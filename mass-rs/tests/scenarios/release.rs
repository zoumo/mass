//! Copied single-file production acceptance, without construction hooks.
use super::*;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::path::{Path, PathBuf};
pub const NETWORK_KEYS: &[&str] = &[
    "HTTP_PROXY",
    "HTTPS_PROXY",
    "ALL_PROXY",
    "NO_PROXY",
    "http_proxy",
    "https_proxy",
    "all_proxy",
    "no_proxy",
    "SSL_CERT_FILE",
    "SSL_CERT_DIR",
];
fn digest(path: &Path) -> String {
    format!("{:x}", Sha256::digest(std::fs::read(path).unwrap()))
}
struct Watch {
    rows: Arc<Mutex<Vec<Value>>>,
    reader: Option<std::thread::JoinHandle<()>>,
}
impl Watch {
    fn new(server: &TestServer) -> Self {
        let mut stream = TcpStream::connect(&server.address).unwrap();
        stream
            .set_read_timeout(Some(Duration::from_secs(180)))
            .unwrap();
        let body=json!({"jsonrpc":"2.0","id":"watch","method":"agentd/session/watch_event","params":{"sessionName":"delivery"}}).to_string();
        write!(stream,"POST /api/v1/agentd/jsonrpc HTTP/1.1\r\nHost: {}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{}",server.address,body.len(),body).unwrap();
        let mut reader = BufReader::new(stream);
        let mut headers = String::new();
        loop {
            let mut line = String::new();
            assert!(reader.read_line(&mut line).unwrap() > 0);
            headers.push_str(&line);
            if line == "\r\n" {
                break;
            }
        }
        assert!(
            headers.contains("200 OK") && headers.contains("text/event-stream"),
            "{headers}"
        );
        let rows = Arc::new(Mutex::new(Vec::new()));
        let received = rows.clone();
        // HTTP chunk size lines are ignored; SSE data lines retain JSON boundaries.
        let task = std::thread::spawn(move || {
            loop {
                let mut line = String::new();
                if reader.read_line(&mut line).unwrap() == 0 {
                    break;
                }
                if let Some(data) = line.strip_prefix("data: ") {
                    received
                        .lock()
                        .unwrap()
                        .push(serde_json::from_str(data.trim()).unwrap());
                }
            }
        });
        Self {
            rows,
            reader: Some(task),
        }
    }
    fn matches(&self, expected: &[Value]) {
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        loop {
            let rows: Vec<_> = self
                .rows
                .lock()
                .unwrap()
                .iter()
                .filter(|e| e.get("seq").is_some())
                .cloned()
                .collect();
            if rows.len() >= expected.len() {
                assert_eq!(rows, expected);
                break;
            }
            assert!(
                std::time::Instant::now() < deadline,
                "SSE missing committed records"
            );
            std::thread::sleep(Duration::from_millis(20));
        }
    }
    fn ended(mut self, path: &Path) {
        let task = self.reader.take().unwrap();
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        while !task.is_finished() {
            assert!(std::time::Instant::now() < deadline, "Stop must end SSE");
            std::thread::sleep(Duration::from_millis(20));
        }
        task.join().unwrap();
        std::fs::write(
            path,
            serde_json::to_vec_pretty(&*self.rows.lock().unwrap()).unwrap(),
        )
        .unwrap();
    }
}
fn prompt(server: &TestServer, text: &str) -> Value {
    let receipt = server.rpc(
        "session/prompt",
        json!({"sessionName":"delivery","prompt":[{"type":"text","text":text}]}),
    );
    assert_eq!(receipt["acceptance"], "native_recorded");
    let deadline = std::time::Instant::now() + Duration::from_secs(180);
    loop {
        let status = server.rpc("session/status", json!({"sessionName":"delivery"}));
        assert_eq!(status["connected"], true);
        if status["state"] == "idle" {
            return receipt;
        }
        assert!(std::time::Instant::now() < deadline);
        std::thread::sleep(Duration::from_millis(100));
    }
}
fn history(server: &TestServer) -> Vec<Value> {
    server.rpc("session/get_event", json!({"sessionName":"delivery"}))["events"]
        .as_array()
        .unwrap()
        .clone()
}
fn evidence(server: &TestServer, root: &Path, phase: &str) {
    for file in ["http.jsonl", "cleanup.json"] {
        std::fs::copy(
            server._directory.path().join(file),
            root.join(format!("{phase}-{file}")),
        )
        .unwrap();
    }
}
#[test]
#[ignore = "copied release binary, reviewed Codex, existing auth, and real model required"]
fn migrated_release() {
    let base = std::env::var_os("MASS_TEST_EVIDENCE")
        .map(PathBuf::from)
        .unwrap_or_else(std::env::temp_dir);
    std::fs::create_dir_all(&base).unwrap();
    let root = tempfile::Builder::new()
        .prefix("mass-migrated_release-")
        .tempdir_in(base)
        .unwrap()
        .keep();
    eprintln!("release evidence: {}", root.display());
    let binary = PathBuf::from(
        std::env::var_os("MASS_TEST_MASSD").expect("MASS_TEST_MASSD release binary required"),
    );
    assert!(binary.is_absolute());
    let manifest_path = binary.parent().unwrap().join("manifest.json");
    let manifest: Value = serde_json::from_slice(&std::fs::read(&manifest_path).unwrap()).unwrap();
    assert_eq!(digest(&binary), manifest["sha256"]);
    std::fs::copy(&manifest_path, root.join("source-manifest.json")).unwrap();
    let help = Command::new(&binary).arg("--help").output().unwrap();
    assert!(help.status.success());
    std::fs::write(root.join("help.txt"), help.stdout).unwrap();
    let reviewed_codex = PathBuf::from(std::env::var_os("MASS_TEST_CODEX").unwrap());
    let sha = std::env::var("MASS_TEST_CODEX_SHA256").unwrap();
    assert_eq!(digest(&reviewed_codex), sha);
    let codex = root.join("reviewed-codex");
    std::fs::copy(&reviewed_codex, &codex).unwrap();
    assert_eq!(digest(&codex), sha);
    let home = std::env::var_os("MASS_TEST_CODEX_HOME")
        .or_else(|| std::env::var_os("CODEX_HOME"))
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from(std::env::var_os("HOME").unwrap()).join(".codex"));
    assert!(
        home.is_absolute() && home.is_dir(),
        "existing Codex auth/config home required"
    );
    let workspace = root.join("workspace");
    std::fs::create_dir(&workspace).unwrap();
    let config = json!({"dataDir":root.join("data"),"runtimes":[{"name":"codex","type":"codex","config":{"command":codex,"sha256":sha,"env":{"CODEX_HOME":home}}}]});
    let mut server = TestServer::prepared_isolated(Some(config.clone()), None, |_| {}, true);
    for path in ["/livez", "/readyz"] {
        assert_eq!(server.get(path).0, 200);
    }
    assert_eq!(server.rpc("status", json!({}))["connected"], 0);
    let session = server.rpc(
        "session/new",
        json!({"sessionName":"delivery","spec":{"runtimeName":"codex","cwd":workspace}}),
    );
    let private: Value = serde_json::from_slice(
        &std::fs::read(session_directory(&root.join("data"), &session).join("metadata.json"))
            .unwrap(),
    )
    .unwrap();
    let runtime = &private["recovery"]["runtime"];
    assert_eq!(runtime["version"], "0.160.1");
    assert_eq!(runtime["sdkRevision"], manifest["codexRevision"]);
    assert_eq!(
        runtime["sdkPatchSha256"],
        manifest["sourceSha256"]["mass-rs/vendor/codex-app-server-client/MASS.patch"]
    );
    assert_eq!(session["capabilities"]["inputTypes"], json!(["text"]));
    assert_eq!(session["capabilities"]["prompt"], true);
    let watch = Watch::new(&server);
    let token = "MASS_RELEASE_CEDAR_1428";
    let receipts = [
        prompt(
            &server,
            &format!(
                "Remember this noncredential token {token}. Without tools or reading files, reply with this token only."
            ),
        ),
        prompt(
            &server,
            "Without tools, repeat only the token from the previous turn.",
        ),
    ];
    let initial = history(&server);
    let journal = session_directory(&root.join("data"), &session).join("events.jsonl");
    let private = |event: &Value| {
        let record = mass_journal::records(&journal, event["seq"].as_u64().unwrap() - 1, 1)
            .unwrap()
            .remove(0);
        assert_eq!(record["event"], *event);
        mass_codex::validate_history_record(
            event["sessionId"].as_str().unwrap(),
            event,
            &record["evidence"],
        )
        .unwrap();
        record["evidence"]["payload"].clone()
    };
    let users: Vec<_> = initial
        .iter()
        .filter(|e| e["type"] == "user.message")
        .collect();
    assert_eq!(users.len(), 2);
    for (event, receipt) in users.iter().zip(&receipts) {
        assert_eq!(event["itemId"], receipt["messageId"]);
        assert_eq!(
            private(event)["source"]["clientId"],
            receipt["submissionId"]
        );
    }
    let replies: Vec<_> = initial
        .iter()
        .filter(|e| e["type"] == "agent.message")
        .collect();
    assert!(replies.len() >= 2);
    for e in replies {
        assert!(e["payload"]["content"].to_string().contains(token));
        assert_eq!(
            e["payload"]["content"],
            json!([{"type":"text","text":private(e)["nativeItem"]["text"]}])
        );
    }
    let journal = session_directory(&root.join("data"), &session).join("events.jsonl");
    let original = std::fs::read(&journal).unwrap();
    assert_eq!(mass_journal::history(&journal, 0, 10000).unwrap(), initial);
    watch.matches(&initial);
    server.stop();
    watch.ended(&root.join("sse.json"));
    evidence(&server, &root, "online");
    // Keep the referenced execution configuration immutable while making only
    // this test's binary copy unavailable. Never move the user's reviewed binary.
    let unavailable = root.join("offline-codex");
    std::fs::rename(&codex, &unavailable).unwrap();
    let mut offline = TestServer::prepared_isolated(Some(config.clone()), None, |_| {}, true);
    assert_eq!(offline.rpc("status", json!({}))["connected"], 0);
    let status = offline.rpc("session/status", json!({"sessionName":"delivery"}));
    assert_eq!(status["connected"], false);
    assert!(status["state"].is_null());
    assert_eq!(history(&offline), initial);
    assert_eq!(std::fs::read(&journal).unwrap(), original);
    assert_eq!(offline.rpc("status", json!({}))["connected"], 0);
    offline.stop();
    evidence(&offline, &root, "offline");
    std::fs::rename(&unavailable, &codex).unwrap();
    let mut resumed = TestServer::prepared_isolated(Some(config), None, |_| {}, true);
    assert_eq!(resumed.rpc("status", json!({}))["connected"], 0);
    let attached = resumed.rpc("session/resume", json!({"sessionName":"delivery"}));
    assert_eq!(attached["sessionId"], session["sessionId"]);
    // The old server retains its observed ownership history after Stop. Compare
    // the resumed server's live observations by PID and start identity instead.
    let previous_native = server.native.lock().unwrap().clone();
    let resumed_native = resumed.native.lock().unwrap().clone();
    assert!(previous_native.contains_key(&(runtime["pid"].as_u64().unwrap() as u32)));
    for (pid, start) in &previous_native {
        assert_ne!(
            resumed_native.get(pid),
            Some(start),
            "cold resume reused an old process identity"
        );
    }
    std::fs::write(
        root.join("process-identities.json"),
        json!({"previous":previous_native,"resumed":resumed_native,"disjoint":true}).to_string(),
    )
    .unwrap();
    assert_eq!(history(&resumed), initial);
    assert_eq!(std::fs::read(&journal).unwrap(), original);
    let watch = Watch::new(&resumed);
    let receipt = prompt(
        &resumed,
        "Without tools or reading files, repeat only the token from before this service restart.",
    );
    let continued = history(&resumed);
    assert_eq!(continued[..initial.len()], initial);
    let added = &continued[initial.len()..];
    let users: Vec<_> = added
        .iter()
        .filter(|e| e["type"] == "user.message")
        .collect();
    assert_eq!(users.len(), 1);
    assert_eq!(users[0]["itemId"], receipt["messageId"]);
    assert!(added.iter().any(
        |e| e["type"] == "agent.message" && e["payload"]["content"].to_string().contains(token)
    ));
    for (index, e) in continued.iter().enumerate() {
        assert_eq!(e["seq"], index + 1);
    }
    assert!(std::fs::read(&journal).unwrap().starts_with(&original));
    assert_eq!(
        mass_journal::history(&journal, 0, 10000).unwrap(),
        continued
    );
    watch.matches(added);
    let final_bytes = std::fs::read(&journal).unwrap();
    resumed.stop();
    watch.ended(&root.join("sse-resumed.json"));
    assert_eq!(std::fs::read(&journal).unwrap(), final_bytes);
    evidence(&resumed, &root, "resumed");
    for server in [&server, &offline, &resumed] {
        let names: Vec<_> = std::fs::read_dir(server._directory.path().join("single-file"))
            .unwrap()
            .map(|e| e.unwrap().file_name())
            .collect();
        assert_eq!(names, [std::ffi::OsString::from("massd")]);
    }
    std::fs::write(
        root.join("history.json"),
        serde_json::to_vec_pretty(&continued).unwrap(),
    )
    .unwrap();
    std::fs::write(root.join("result.json"),json!({"passed":true,"rustTest":"release::migrated_release","productionBinarySha256":digest(&binary),"sourceManifestSha256":digest(&manifest_path),"daemonPath":"/usr/bin:/bin","privateHome":true,"offlineExactHistory":true,"coldResumeAppend":true,"preservedPrefixCount":initial.len(),"durableCount":continued.len()}).to_string()).unwrap();
}
