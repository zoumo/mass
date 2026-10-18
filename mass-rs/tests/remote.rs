//! Deterministic protocol fixture. It proves MASS policy, not real server authentication.
use futures_util::{SinkExt, StreamExt};
use serde_json::{Value, json};
use std::{collections::BTreeMap, sync::Arc, time::Duration};
use tokio_tungstenite::tungstenite::Message;

fn thread_result(id: &str, cwd: &Value) -> Value {
    json!({"thread":{"id":id,"sessionId":id,"preview":"","ephemeral":false,"modelProvider":"openai","createdAt":0,"updatedAt":0,"status":{"type":"idle"},"cwd":cwd,"cliVersion":"0.160.1","source":"vscode","turns":[]},"model":"fixture","modelProvider":"openai","cwd":cwd,"approvalPolicy":"untrusted","approvalsReviewer":"user","sandbox":{"type":"workspaceWrite","writableRoots":[],"networkAccess":false,"excludeTmpdirEnvVar":false,"excludeSlashTmp":false}})
}
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn remote_connection_stop_and_eof_never_prove_thread_release_or_restore_ownership() {
    let root = tempfile::tempdir().unwrap();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("ws://{}", listener.local_addr().unwrap());
    let observed = Arc::new(std::sync::Mutex::new(Vec::<Value>::new()));
    let rows = observed.clone();
    let (disconnect, _) = tokio::sync::watch::channel(false);
    let drop_signal = disconnect.clone();
    let server = tokio::spawn(async move {
        let mut tasks = tokio::task::JoinSet::new();
        let mut index = 0;
        loop {
            tokio::select! {
                accepted=listener.accept()=>{
                    let (stream,_)=accepted.unwrap();index+=1;let connection=index;
                    let rows=rows.clone();let mut loss=drop_signal.subscribe();
                    tasks.spawn(async move {
                        let mut ws=tokio_tungstenite::accept_async(stream).await.unwrap();
                        let mut count=0;
                        loop { tokio::select! {
                            _=loss.changed()=>break,
                            frame=ws.next()=>{
                                let Some(Ok(Message::Text(text)))=frame else { break; };
                                let request:Value=serde_json::from_str(&text).unwrap();rows.lock().unwrap().push(request.clone());
                                let result=match request["method"].as_str().unwrap() {
                                    "initialize"=>json!({"userAgent":"codex/0.160.1"}),
                                    "initialized"=>continue,
                                    "thread/start"=>{count+=1;thread_result(&format!("opaque/agent:{connection}:{count}"),&request["params"]["cwd"])},
                                    "thread/unsubscribe"=>json!({"status":"unsubscribed"}), // ACK alone is insufficient.
                                    other=>panic!("unexpected method {other}"),
                                };
                                if ws.send(Message::text(json!({"id":request["id"],"result":result}).to_string())).await.is_err(){break;}
                            }
                        }}
                    });
                }
                Some(_)=tasks.join_next()=>{}
            }
        }
    });
    let config: mass_codex::RemoteRegistrationConfig =
        serde_json::from_value(json!({"url":url.clone(),"closeTimeoutMs":30})).unwrap();
    let one = Arc::new(config.clone().runtime(mass_codex::Observation {
        directory: Some(root.path().join("one")),
        ..Default::default()
    }));
    let two = Arc::new(config.runtime(Default::default()));
    let service = mass_sessions::Service::new(
        root.path().join("data"),
        BTreeMap::from([("one".into(), one.clone()), ("two".into(), two.clone())]),
        vec![],
        Duration::from_millis(50),
    )
    .unwrap();
    let create = |name: &str, runtime: &str| json!({"sessionName":name,"spec":{"runtimeName":runtime,"cwd":"/remote/does-not-exist-on-mass"}});
    let first = service
        .call("agentd/session/new", create("first", "one"))
        .await
        .unwrap();
    assert_eq!(first["sessionId"], "opaque/agent:1:1");
    assert_eq!(first["capabilities"]["resume"], false);
    let peer = service
        .call("agentd/session/new", create("peer", "two"))
        .await
        .unwrap();
    assert_ne!(peer["sessionId"], first["sessionId"]);
    let archive = service
        .call("agentd/session/archive", json!({"sessionName":"first"}))
        .await
        .unwrap_err()
        .response();
    assert_eq!(archive["data"]["kind"], "close_unknown");
    one.stop().await;
    assert_eq!(one.status().await["phase"], "stopped");
    let status = service
        .call("agentd/session/status", json!({"sessionName":"first"}))
        .await
        .unwrap();
    assert_eq!(status["lifecycle"], "closing");
    assert_eq!(status["capabilities"]["resume"], false);
    // A distinct live client still creates work after the first connection is gone.
    service
        .call("agentd/session/new", create("peer-next", "two"))
        .await
        .unwrap();
    disconnect.send(true).unwrap();
    tokio::time::timeout(Duration::from_secs(3), async {
        while two.status().await["phase"] != "error" {
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    })
    .await
    .unwrap();
    let status = service
        .call("agentd/session/status", json!({"sessionName":"peer"}))
        .await
        .unwrap();
    assert_eq!(status["connected"], false);
    assert_eq!(status["capabilities"]["resume"], false);
    let error = service
        .call("agentd/session/resume", json!({"sessionName":"peer"}))
        .await
        .unwrap_err()
        .response();
    assert_eq!(error["data"]["kind"], "unsupported_capability");
    assert!(
        !observed
            .lock()
            .unwrap()
            .iter()
            .any(|r| r["method"] == "thread/resume")
    );
    assert_eq!(
        service
            .call("agentd/session/get_event", json!({"sessionName":"peer"}))
            .await
            .unwrap()["events"],
        json!([])
    );
    service.shutdown().await;
    let cleanup = std::fs::read_dir(root.path().join("one"))
        .unwrap()
        .map(|e| e.unwrap().path().join("cleanup.json"))
        .find(|p| p.exists())
        .unwrap();
    let cleanup: Value = serde_json::from_slice(&std::fs::read(cleanup).unwrap()).unwrap();
    assert_eq!(cleanup["localConnectionEnded"], true);
    assert_eq!(cleanup["processReaped"], false);
    assert_eq!(cleanup["remoteWorkOutcome"], "unknown");
    let cold_runtime: mass_codex::RemoteRegistrationConfig =
        serde_json::from_value(json!({"url":url.clone(),"closeTimeoutMs":30})).unwrap();
    let cold = mass_sessions::Service::new(
        root.path().join("data"),
        BTreeMap::from([
            (
                "one".into(),
                Arc::new(cold_runtime.clone().runtime(Default::default())),
            ),
            (
                "two".into(),
                Arc::new(cold_runtime.runtime(Default::default())),
            ),
        ]),
        vec![],
        Duration::from_millis(50),
    )
    .unwrap();
    assert_eq!(
        cold.call("agentd/session/status", json!({"sessionName":"peer"}))
            .await
            .unwrap()["capabilities"]["resume"],
        false
    );
    let error = cold
        .call("agentd/session/resume", json!({"sessionName":"peer"}))
        .await
        .unwrap_err()
        .response();
    assert_eq!(error["data"]["kind"], "unsupported_capability");
    assert_eq!(
        cold.call("agentd/runtime/status", json!({"name":"two"}))
            .await
            .unwrap()["status"]["phase"],
        "pending"
    );
    cold.shutdown().await;
    server.abort();
    let _ = server.await;
}

#[test]
#[allow(clippy::result_large_err)] // Official handshake callback requires an HTTP Response error.
fn remote_auth_ref_header_and_handshake_failure_are_redacted_with_an_arbitrary_source_name() {
    // Environment mutation is restricted to a fresh test subprocess, never the
    // daemon's shared process or a user's shell/configuration.
    if std::env::var_os("MASS_REMOTE_AUTH_FIXTURE_CHILD").is_none() {
        let output=std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact","remote_auth_ref_header_and_handshake_failure_are_redacted_with_an_arbitrary_source_name","--nocapture"])
            .env("MASS_REMOTE_AUTH_FIXTURE_CHILD","1").env("MASS_AUTH_SOURCE","dummy-auth-canary-ticket06")
            .output().unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        return;
    }
    tokio::runtime::Runtime::new().unwrap().block_on(async {
        let root=tempfile::tempdir().unwrap();
        let listener=tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url=format!("ws://{}",listener.local_addr().unwrap());
        let server=tokio::spawn(async move {
            let (stream,_)=listener.accept().await.unwrap();
            let mut ws=tokio_tungstenite::accept_hdr_async(stream,|request:&tokio_tungstenite::tungstenite::handshake::server::Request,response| {
                assert_eq!(request.headers()["Authorization"],"Bearer dummy-auth-canary-ticket06");Ok(response)
            }).await.unwrap();
            let request:Value=serde_json::from_str(ws.next().await.unwrap().unwrap().to_text().unwrap()).unwrap();
            ws.send(Message::text(json!({"id":request["id"],"error":{"code":-32000,"message":"upstream echo dummy-auth-canary-ticket06"}}).to_string())).await.unwrap();
        });
        let config:mass_codex::RemoteRegistrationConfig=serde_json::from_value(json!({"url":url,"authRef":"env:MASS_AUTH_SOURCE"})).unwrap();
        let runtime=config.runtime(mass_codex::Observation{directory:Some(root.path().join("observed")),..Default::default()});
        let error=runtime.start().await.err().unwrap().to_string();
        assert!(!error.contains("canary"));assert!(!runtime.status().await.to_string().contains("canary"));
        runtime.stop().await;server.await.unwrap();
        fn inspect(root:&std::path::Path) {
            for entry in std::fs::read_dir(root).unwrap(){let entry=entry.unwrap();if entry.file_type().unwrap().is_dir(){inspect(&entry.path());}else{assert!(!std::fs::read_to_string(entry.path()).unwrap().contains("dummy-auth-canary-ticket06"));}}
        }
        inspect(root.path());
    });
}
