//! Test-owned real server; MASS owns connections, this fixture alone owns the child.
use crate::support::processes;
use futures_util::FutureExt;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{collections::BTreeMap, path::PathBuf, process::Stdio, sync::Arc, time::Duration};

struct Client {
    address: std::net::SocketAddr,
    client: reqwest::Client,
    transcript: std::sync::Mutex<std::fs::File>,
}
impl Client {
    async fn call(&self, method: &str, name: &str, mut params: Value) -> Value {
        params["sessionName"] = json!(name);
        let response:Value=self.client.post(format!("http://{}/api/v1/agentd/jsonrpc",self.address))
            .json(&json!({"jsonrpc":"2.0","id":uuid::Uuid::now_v7().to_string(),"method":format!("agentd/{method}"),"params":params})).send().await.unwrap().json().await.unwrap();
        use std::io::Write;
        writeln!(
            self.transcript.lock().unwrap(),
            "{}",
            json!({"method":method,"params":params,"response":response})
        )
        .unwrap();
        response
    }
    async fn result(&self, method: &str, name: &str, params: Value) -> Value {
        let response = self.call(method, name, params).await;
        assert!(response.get("result").is_some(), "{method}: {response}");
        response["result"].clone()
    }
    async fn history(&self, name: &str) -> Vec<Value> {
        self.result("session/get_event", name, json!({"limit":1000}))
            .await["events"]
            .as_array()
            .unwrap()
            .clone()
    }
    async fn idle(&self, name: &str) {
        tokio::time::timeout(Duration::from_secs(180), async {
            loop {
                let status = self.result("session/status", name, json!({})).await;
                assert_eq!(status["connected"], true, "{status}");
                if status["state"] == "idle" {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(100)).await;
            }
        })
        .await
        .unwrap();
    }
    async fn prompt(&self, name: &str, text: &str) -> Value {
        let receipt = self
            .result(
                "session/prompt",
                name,
                json!({"prompt":[{"type":"text","text":text}]}),
            )
            .await;
        assert_eq!(receipt["acceptance"], "native_recorded");
        receipt
    }
}
fn all_files(root: &std::path::Path, name: &str, out: &mut Vec<PathBuf>) {
    for entry in std::fs::read_dir(root).unwrap() {
        let entry = entry.unwrap();
        if entry.file_type().unwrap().is_dir() {
            all_files(&entry.path(), name, out);
        } else if entry.file_name() == name {
            out.push(entry.path());
        }
    }
}
pub async fn run(disconnect: bool) {
    let binary = PathBuf::from(std::env::var("MASS_TEST_CODEX").unwrap());
    let sha = std::env::var("MASS_TEST_CODEX_SHA256").unwrap();
    assert!(binary.is_absolute());
    assert_eq!(
        format!("{:x}", Sha256::digest(std::fs::read(&binary).unwrap())),
        sha
    );
    let version = tokio::process::Command::new(&binary)
        .arg("--version")
        .output()
        .await
        .unwrap();
    assert!(version.status.success());
    assert_eq!(
        String::from_utf8_lossy(&version.stdout).trim(),
        "codex-cli 0.160.1"
    );
    let base = std::env::var_os("MASS_TEST_EVIDENCE")
        .map(PathBuf::from)
        .unwrap_or_else(std::env::temp_dir);
    let root = tempfile::Builder::new()
        .prefix("mass-remote-")
        .tempdir_in(base)
        .unwrap()
        .keep();
    eprintln!("Remote native evidence: {}", root.display());
    let shell = root.join("shell");
    std::fs::create_dir(&shell).unwrap();
    let port = std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port();
    let url = format!("ws://127.0.0.1:{port}");
    let mut child = tokio::process::Command::new(&binary)
        .args([
            "app-server",
            "--listen",
            &url,
            "-c",
            "thread_unload_delay_secs=0",
        ])
        .current_dir(&root)
        .env("ZDOTDIR", shell)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .kill_on_drop(true)
        .spawn()
        .unwrap();
    let pid = child.id().unwrap();
    let identity = processes()[&pid].clone();
    std::fs::write(root.join("server.json"),json!({"owner":"test_fixture_only","identity":identity,"binary":binary,"sha256":sha,"endpoint":url,"authentication":"none-loopback-only","version":"0.160.1"}).to_string()).unwrap();
    let config: mass_codex::RemoteRegistrationConfig =
        serde_json::from_value(json!({"url":url,"closeTimeoutMs":1000})).unwrap();
    let remote = Arc::new(config.clone().runtime(mass_codex::Observation {
        directory: Some(root.join("remote")),
        disconnect_interrupt: disconnect,
        ..Default::default()
    }));
    let peer = Arc::new(config.runtime(mass_codex::Observation {
        directory: Some(root.join("peer")),
        ..Default::default()
    }));
    let service = Arc::new(
        mass_sessions::Service::new(
            root.join("data"),
            BTreeMap::from([
                ("remote".into(), remote.clone()),
                ("peer".into(), peer.clone()),
            ]),
            vec![],
            Duration::from_secs(3),
        )
        .unwrap(),
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let app = mass_http::router(service.clone());
    let http = tokio::spawn(async move { axum::serve(listener, app).await });
    let client = Client {
        address,
        client: reqwest::Client::builder()
            .no_proxy()
            .timeout(Duration::from_secs(45))
            .build()
            .unwrap(),
        transcript: std::sync::Mutex::new(std::fs::File::create(root.join("rpc.jsonl")).unwrap()),
    };
    let result=std::panic::AssertUnwindSafe(async {
        tokio::time::timeout(Duration::from_secs(10),async {loop {
            assert!(child.try_wait().unwrap().is_none(),"fixture server exited before ready");
            if tokio::net::TcpStream::connect(("127.0.0.1",port)).await.is_ok(){break;}
            tokio::time::sleep(Duration::from_millis(20)).await;
        }}).await.unwrap();
        let new=|runtime:&str|json!({"spec":{"runtimeName":runtime,"cwd":root}});
        let target=client.result("session/new","target",new("remote")).await;
        let other=client.result("session/new","peer",new("peer")).await;
        assert_ne!(target["sessionId"],other["sessionId"]);assert_eq!(target["capabilities"]["resume"],false);
        if !disconnect {
            let receipt=client.prompt("target","Use only the shell command execution tool. Execute exactly: printf 'MASS_REMOTE_TOOL_06\\n' > remote-tool.txt . Then read remote-tool.txt with cat using one separate shell call. Finally reply MASS_REMOTE_TEXT_06. Do not inspect other files or use other tools.").await;
            client.idle("target").await;
            assert_eq!(std::fs::read_to_string(root.join("remote-tool.txt")).unwrap(),"MASS_REMOTE_TOOL_06\n");
            let history=client.history("target").await;
            assert!(history.iter().any(|e|e["type"]=="user.message" && e["itemId"]==receipt["messageId"]));
            for kind in ["agent.message","agent.tool_call","agent.tool_result"]{assert!(history.iter().any(|e|e["type"]==kind),"{kind}");}
            client.result("session/archive","target",json!({})).await;
            assert_eq!(client.result("session/status","target",json!({})).await["capabilities"]["resume"],false);
            let error=client.call("session/resume","target",json!({})).await;assert_eq!(error["error"]["data"]["kind"],"unsupported_capability");
            assert_eq!(client.history("target").await,history);
            client.result("session/new","active",new("remote")).await;
        }
        let name=if disconnect {"target"} else {"active"};
        client.prompt(name,"Use only the shell command execution tool. Execute exactly sleep 120 as one shell call, then give a short reply. Do not inspect files or run other commands.").await;
        tokio::time::timeout(Duration::from_secs(120),async {loop {
            if client.history(name).await.iter().any(|e|e["type"]=="agent.tool_call"){break;}
            tokio::time::sleep(Duration::from_millis(50)).await;
        }}).await.unwrap();
        let cancelled=client.call("session/cancel",name,json!({})).await;
        if disconnect {
            assert_eq!(cancelled["error"]["data"]["kind"],"cancel_unknown");
            let close=client.call("session/archive",name,json!({})).await;assert_eq!(close["error"]["data"]["kind"],"close_unknown");
        } else { assert!(cancelled.get("result").is_some(),"{cancelled}");client.idle(name).await; }
        remote.stop().await;
        assert_eq!(remote.status().await["phase"],"stopped");
        assert_eq!(client.result("session/status",name,json!({})).await["lifecycle"],"closing");
        assert!(child.try_wait().unwrap().is_none(),"MASS must not stop the shared server");
        assert_eq!(processes()[&pid]["start"],identity["start"]);
        client.prompt("peer","Without tools reply exactly MASS_REMOTE_PEER_SURVIVES_06.").await;client.idle("peer").await;
        assert!(client.history("peer").await.iter().any(|e|e["type"]=="agent.message" && e["payload"].to_string().contains("MASS_REMOTE_PEER_SURVIVES_06")));
        client.result("session/archive","peer",json!({})).await;
        let old_history=client.history("target").await;
        std::fs::write(root.join("proof.json"),json!({"target":target,"peer":other,"history":old_history,"serverAliveAfterRemoteStop":true,"peerCompletedAfterRemoteStop":true,"remoteRecoverySupported":false,"disconnect":disconnect}).to_string()).unwrap();
        // Each public ID is the exact response from this connection's native start.
        for (directory,session) in [("remote",&target),("peer",&other)] {
            let mut files=Vec::new();all_files(&root.join(directory),"wire.jsonl",&mut files);
            let wire:Vec<Value>=files.iter().flat_map(|p|std::fs::read_to_string(p).unwrap().lines().map(|l|serde_json::from_str::<Value>(l).unwrap()).collect::<Vec<_>>()).collect();
            assert!(wire.iter().any(|r|r["payload"]["result"]["thread"]["id"]==session["sessionId"]));
            assert!(!wire.iter().any(|r|r["payload"]["method"]=="thread/resume"));
        }
    }).catch_unwind().await;
    service.shutdown().await;
    http.abort();
    let _ = http.await;
    let alive_after_stop = child.try_wait().unwrap().is_none();
    // Only the fixture owner terminates the externally supplied process, after
    // product cleanup and its independent peer-survival assertion.
    let observed = crate::support::process::descendants(&processes(), pid);
    assert_eq!(observed[&pid]["start"], identity["start"]);
    let mut signals = Vec::new();
    // Any test tool descendants also belong to this explicit fixture. Verify
    // PID/start before each signal; never signal unrelated or reused identities.
    for (&descendant, row) in &observed {
        if descendant != pid
            && processes()
                .get(&descendant)
                .is_some_and(|v| v["start"] == row["start"])
        {
            let success = std::process::Command::new("/bin/kill")
                .args(["-TERM", &descendant.to_string()])
                .status()
                .unwrap()
                .success();
            signals.push(json!({"identity":row,"signal":"TERM","success":success}));
        }
    }
    child.start_kill().unwrap();
    signals.push(json!({"identity":identity,"signal":"KILL","success":true}));
    let reaped = child.wait().await.unwrap();
    let until = tokio::time::Instant::now() + Duration::from_secs(3);
    loop {
        let table = processes();
        if observed
            .iter()
            .all(|(p, r)| table.get(p).is_none_or(|v| v["start"] != r["start"]))
            || tokio::time::Instant::now() >= until
        {
            break;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    let table = processes();
    let remaining: Vec<_> = observed
        .iter()
        .filter(|(p, r)| table.get(p).is_some_and(|v| v["start"] == r["start"]))
        .map(|(_, r)| r.clone())
        .collect();
    std::fs::write(root.join("fixture-cleanup.json"),json!({"owner":"test_fixture_only","aliveAfterProductStop":alive_after_stop,"ownedBeforeShutdown":observed.values().collect::<Vec<_>>(),"remainingOwned":remaining,"fixtureSignals":signals,"serverReaped":true,"exit":reaped.to_string()}).to_string()).unwrap();
    assert!(
        remaining.is_empty(),
        "test-owned process descendants survived fixture cleanup"
    );
    if let Err(panic) = result {
        std::panic::resume_unwind(panic);
    }
    std::fs::write(root.join("result.json"),json!({"passed":true,"disconnect":disconnect,"authenticationAcceptance":"not-verified-no-authorized-authenticated-service","realLoopbackProtocol":true}).to_string()).unwrap();
}
