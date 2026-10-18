use std::collections::BTreeMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpStream;
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex, mpsc};
#[path = "support/process.rs"]
mod process;
#[path = "scenarios/release.rs"]
mod release;
use std::time::Duration;

struct TestServer {
    child: Child,
    address: String,
    _directory: tempfile::TempDir,
    native: Mutex<BTreeMap<u32, String>>,
    observed: Arc<Mutex<BTreeMap<u32, serde_json::Value>>>,
    sampling: Arc<std::sync::atomic::AtomicBool>,
    sampler: Option<std::thread::JoinHandle<()>>,
}

impl TestServer {
    fn start() -> Self {
        Self::configured(None)
    }

    fn configured(config: Option<serde_json::Value>) -> Self {
        Self::prepared(config, None, |_| {})
    }
    fn prepared(
        config: Option<serde_json::Value>,
        config_subdirectory: Option<&str>,
        prepare: impl FnOnce(&std::path::Path),
    ) -> Self {
        Self::prepared_isolated(config, config_subdirectory, prepare, false)
    }
    fn prepared_isolated(
        config: Option<serde_json::Value>,
        config_subdirectory: Option<&str>,
        prepare: impl FnOnce(&std::path::Path),
        isolated: bool,
    ) -> Self {
        let directory = tempfile::tempdir().expect("isolated daemon cwd/data");
        prepare(directory.path());
        // Native release acceptance can copy only the reviewed executable into
        // an isolated launch directory. Ordinary health tests always use Cargo's binary.
        let binary = if config.is_some()
            && let Some(source) = std::env::var_os("MASS_TEST_MASSD")
        {
            let source = std::path::PathBuf::from(source);
            assert!(source.is_absolute(), "MASS_TEST_MASSD must be absolute");
            let launch = if isolated {
                let path = directory.path().join("single-file");
                std::fs::create_dir(&path).unwrap();
                path
            } else {
                directory.path().into()
            };
            let copied = launch.join("massd");
            std::fs::copy(source, &copied)
                .expect("copy release daemon without source/cache/runtime files");
            copied
        } else {
            std::path::PathBuf::from(env!("CARGO_BIN_EXE_massd"))
        };
        let mut command = Command::new(binary);
        command
            .args(["--listen", "127.0.0.1:0"])
            .current_dir(directory.path());
        if let Some(config) = config {
            let parent = if let Some(subdirectory) = config_subdirectory {
                let path = directory.path().join(subdirectory);
                std::fs::create_dir_all(&path).unwrap();
                path
            } else {
                directory.path().into()
            };
            let path = parent.join("config.json");
            std::fs::write(&path, config.to_string()).unwrap();
            command.arg("--config").arg(path);
        }
        if isolated {
            command.current_dir(directory.path().join("single-file"));
            assert!(
                std::env::var_os("MASS_TEST_MASSD").is_some(),
                "reviewed release binary required"
            );
            command
                .env_clear()
                .env("PATH", "/usr/bin:/bin")
                .env("HOME", directory.path())
                .env("TMPDIR", "/tmp");
            for key in release::NETWORK_KEYS {
                if let Some(value) = std::env::var_os(key) {
                    command.env(key, value);
                }
            }
        }
        let mut child = command
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .spawn()
            .expect("start massd");
        let stdout = child.stdout.take().expect("server stdout");
        let observed = Arc::new(Mutex::new(BTreeMap::new()));
        let sampling = Arc::new(std::sync::atomic::AtomicBool::new(true));
        let identity = process::table().get(&child.id()).unwrap().clone();
        observed
            .lock()
            .unwrap()
            .insert(child.id(), identity.clone());
        let sampler = {
            let observed = observed.clone();
            let sampling = sampling.clone();
            std::thread::spawn(move || {
                while sampling.load(std::sync::atomic::Ordering::Relaxed) {
                    let table = process::table();
                    let pid = identity["pid"].as_u64().unwrap() as u32;
                    if table
                        .get(&pid)
                        .is_some_and(|row| row["start"] == identity["start"])
                    {
                        observed
                            .lock()
                            .unwrap()
                            .extend(process::descendants(&table, pid));
                    }
                    std::thread::sleep(Duration::from_millis(100));
                }
            })
        };
        let mut server = Self {
            child,
            address: String::new(),
            _directory: directory,
            native: Mutex::new(BTreeMap::new()),
            observed,
            sampling,
            sampler: Some(sampler),
        };
        let (sender, receiver) = mpsc::channel();
        std::thread::spawn(move || {
            let mut line = String::new();
            BufReader::new(stdout)
                .read_line(&mut line)
                .expect("read startup address");
            let _ = sender.send(line);
        });
        let line = receiver
            .recv_timeout(Duration::from_secs(5))
            .expect("server starts within five seconds");
        server.address = line
            .trim()
            .strip_prefix("Listening on http://")
            .expect("server reports bound HTTP address")
            .to_owned();
        server
    }

    fn get(&self, path: &str) -> (u16, serde_json::Value) {
        self.request("GET", path, "")
    }

    fn rpc(&self, method: &str, params: serde_json::Value) -> serde_json::Value {
        let request = serde_json::json!({"jsonrpc":"2.0","id":1,"method":format!("agentd/{method}"),"params":params});
        let (status, response) =
            self.request("POST", "/api/v1/agentd/jsonrpc", &request.to_string());
        assert_eq!(status, 200);
        assert!(response.get("result").is_some(), "{response}");
        if response["result"]["connected"] == true {
            assert!(response["result"].get("nativeSessionId").is_none());
            assert!(response["result"].get("runtime").is_none());
            let table = process::table();
            for (pid, row) in process::descendants(&table, self.child.id()) {
                self.native
                    .lock()
                    .unwrap()
                    .insert(pid, row["start"].as_str().unwrap().to_owned());
            }
        }
        let record = serde_json::json!({"request":request,"response":response});
        writeln!(
            std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(self._directory.path().join("http.jsonl"))
                .unwrap(),
            "{record}"
        )
        .unwrap();
        response["result"].clone()
    }

    fn request(&self, method: &str, path: &str, body: &str) -> (u16, serde_json::Value) {
        let mut stream = TcpStream::connect(&self.address).expect("connect to server");
        stream
            .set_read_timeout(Some(Duration::from_secs(45)))
            .expect("set read timeout");
        stream
            .set_write_timeout(Some(Duration::from_secs(5)))
            .unwrap();
        write!(
            stream,
            "{method} {path} HTTP/1.1\r\nHost: {}\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{body}",
            self.address,body.len()
        )
        .expect("send request");
        let mut response = String::new();
        stream
            .read_to_string(&mut response)
            .expect("read HTTP response");
        let (headers, body) = response
            .split_once("\r\n\r\n")
            .expect("HTTP response headers");
        let status = headers
            .split_whitespace()
            .nth(1)
            .expect("status code")
            .parse()
            .expect("numeric status");
        (
            status,
            serde_json::from_str(body).expect("JSON response body"),
        )
    }

    fn stop(&mut self) {
        let status = self
            .graceful_stop()
            .expect("stop owned daemon")
            .expect("daemon graceful shutdown deadline");
        assert!(status.success());
        self.sampling
            .store(false, std::sync::atomic::Ordering::Relaxed);
        if let Some(sampler) = self.sampler.take() {
            sampler.join().unwrap();
        }
        for (pid, start) in self.native.lock().unwrap().iter() {
            assert_ne!(
                process_start(*pid)
                    .expect("read native cleanup identity")
                    .as_ref(),
                Some(start),
                "owned native process remains after daemon shutdown"
            );
        }
        // Stop has reaped the daemon and its native hosts. External hooks can
        // finish just after their host; observe bounded exit without signals.
        let owned = self.observed.lock().unwrap().clone();
        let settle = std::time::Instant::now();
        let table = loop {
            let table = process::table();
            if !owned.iter().any(|(pid, row)| {
                table
                    .get(pid)
                    .is_some_and(|now| now["start"] == row["start"])
            }) || settle.elapsed() >= Duration::from_secs(5)
            {
                break table;
            }
            std::thread::sleep(Duration::from_millis(100));
        };
        let remaining: Vec<_> = owned
            .iter()
            .filter(|(pid, row)| {
                table
                    .get(pid)
                    .is_some_and(|now| now["start"] == row["start"])
            })
            .map(|(_, row)| row.clone())
            .collect();
        std::fs::write(self._directory.path().join("cleanup.json"),serde_json::json!({"observedOwned":owned.values().collect::<Vec<_>>(),"remainingOwned":remaining,"harnessCleanupSignals":[],"descendantSettleMs":settle.elapsed().as_millis()}).to_string()).unwrap();
        assert!(
            remaining.is_empty(),
            "product Stop left observed descendants: {remaining:?}"
        );
    }
    fn graceful_stop(&mut self) -> std::io::Result<Option<std::process::ExitStatus>> {
        if let Some(status) = self.child.try_wait()? {
            return Ok(Some(status));
        }
        let _ = Command::new("/bin/kill")
            .args(["-TERM", &self.child.id().to_string()])
            .stderr(Stdio::null())
            .status();
        let deadline = std::time::Instant::now() + Duration::from_secs(30);
        loop {
            if let Some(status) = self.child.try_wait()? {
                return Ok(Some(status));
            }
            if std::time::Instant::now() >= deadline {
                return Ok(None);
            }
            std::thread::sleep(Duration::from_millis(20));
        }
    }
}

impl Drop for TestServer {
    fn drop(&mut self) {
        let _ = self.graceful_stop();
        let _ = self.child.kill();
        let _ = self.child.wait();
        self.sampling
            .store(false, std::sync::atomic::Ordering::Relaxed);
        if let Some(sampler) = self.sampler.take() {
            let _ = sampler.join();
        }
        if let Ok(owned) = self.observed.lock() {
            let table = process::table();
            for (pid, row) in owned.iter() {
                if table
                    .get(pid)
                    .is_some_and(|now| now["start"] == row["start"])
                {
                    let _ = Command::new("/bin/kill")
                        .args(["-KILL", &pid.to_string()])
                        .status();
                }
            }
        }
        if std::thread::panicking() {
            let directory = std::mem::replace(&mut self._directory, tempfile::tempdir().unwrap());
            eprintln!("failed daemon evidence: {}", directory.keep().display());
        }
        // Panic cleanup may signal only native PID/start identities captured by
        // this test's successful session opens, never a stale saved metadata PID.
        if let Ok(native) = self.native.get_mut() {
            for (pid, start) in native {
                if process_start(*pid).ok().flatten().as_ref() == Some(start) {
                    let _ = Command::new("/bin/kill")
                        .args(["-KILL", &pid.to_string()])
                        .stderr(Stdio::null())
                        .status();
                }
            }
        }
    }
}

fn process_start(pid: u32) -> std::io::Result<Option<String>> {
    let output = Command::new("ps")
        .args(["-p", &pid.to_string(), "-o", "lstart="])
        .output()?;
    if !output.status.success() && output.status.code() != Some(1) {
        return Err(std::io::Error::other("ps identity readback failed"));
    }
    let value = String::from_utf8_lossy(&output.stdout).trim().to_owned();
    Ok((!value.is_empty()).then_some(value))
}

#[test]
fn empty_service_is_alive_and_ready_without_claiming_an_agent_connection() {
    let server = TestServer::start();
    assert!(
        server._directory.path().join("data/sessions").is_dir(),
        "default data is relative to the isolated launch cwd"
    );
    let (status, body) = server.get("/livez");
    assert_eq!(status, 200);
    assert_eq!(body, serde_json::json!({"status": "alive"}));

    let (status, body) = server.get("/readyz");
    assert_eq!(status, 200);
    assert_eq!(
        body,
        serde_json::json!({
            "status": "ready",
            "agents": {"configured": 0, "connected": 0}
        })
    );
}

fn copy_fixture(source: &std::path::Path, destination: &std::path::Path) {
    std::fs::create_dir_all(destination).unwrap();
    for entry in std::fs::read_dir(source).unwrap() {
        let entry = entry.unwrap();
        let kind = entry.file_type().unwrap();
        let target = destination.join(entry.file_name());
        if kind.is_dir() {
            copy_fixture(&entry.path(), &target);
        } else {
            assert!(kind.is_file(), "fixture must not contain symlinks/devices");
            std::fs::copy(entry.path(), target).unwrap();
        }
    }
}
fn snapshot(root: &std::path::Path) -> BTreeMap<std::path::PathBuf, Vec<u8>> {
    fn walk(
        base: &std::path::Path,
        path: &std::path::Path,
        result: &mut BTreeMap<std::path::PathBuf, Vec<u8>>,
    ) {
        for entry in std::fs::read_dir(path).unwrap() {
            let entry = entry.unwrap();
            if entry.file_type().unwrap().is_dir() {
                walk(base, &entry.path(), result);
            } else {
                result.insert(
                    entry.path().strip_prefix(base).unwrap().into(),
                    std::fs::read(entry.path()).unwrap(),
                );
            }
        }
    }
    let mut result = BTreeMap::new();
    walk(root, root, &mut result);
    result
}
fn offline_source() -> &'static std::path::Path {
    static SOURCE: std::sync::OnceLock<std::path::PathBuf> = std::sync::OnceLock::new();
    SOURCE.get_or_init(|| {
        use serde_json::json;
        let base=std::env::var_os("MASS_TEST_EVIDENCE").map(std::path::PathBuf::from).unwrap_or_else(std::env::temp_dir);std::fs::create_dir_all(&base).unwrap();
        let root=tempfile::Builder::new().prefix("fixture-offline-source-").tempdir_in(base).unwrap().keep();std::fs::create_dir(root.join("workspace")).unwrap();
        let config=json!({"dataDir":root.join("data"),"runtimes":[{"name":"codex","type":"codex","config":{"command":std::env::var("MASS_TEST_CODEX").expect("reviewed Codex required"),"sha256":std::env::var("MASS_TEST_CODEX_SHA256").expect("reviewed SHA256 required")}}]});
        let mut server=TestServer::configured(Some(config));
        let session=server.rpc("session/new",json!({"sessionName":"saved","spec":{"runtimeName":"codex","cwd":root.join("workspace")}}));
        server.rpc("session/prompt",json!({"sessionName":"saved","prompt":[{"type":"text","text":"Without tools, reply only ORIGINAL_OFFLINE_CEDAR_915."}]}));
        let deadline=std::time::Instant::now()+Duration::from_secs(180);
        while server.rpc("session/status",json!({"sessionName":"saved"}))["state"]!="idle" {assert!(std::time::Instant::now()<deadline);std::thread::sleep(Duration::from_millis(100));}
        let history=server.rpc("session/get_event",json!({"sessionName":"saved"}))["events"].clone();server.stop();
        for file in ["http.jsonl","cleanup.json"] {std::fs::copy(server._directory.path().join(file),root.join(file)).unwrap();}
        std::fs::write(root.join("fixture.json"),json!({"session":session,"history":history}).to_string()).unwrap();
        root
    })
}
fn offline_case(mode: &str) {
    use serde_json::json;
    use sha2::{Digest, Sha256};
    let source = offline_source();
    let fixture: serde_json::Value =
        serde_json::from_slice(&std::fs::read(source.join("fixture.json")).unwrap()).unwrap();
    let session = &fixture["session"];
    let expected = fixture["history"].as_array().unwrap();
    let before = snapshot(&source.join("data"));
    let base = std::env::var_os("MASS_TEST_EVIDENCE")
        .map(std::path::PathBuf::from)
        .unwrap_or_else(std::env::temp_dir);
    std::fs::create_dir_all(&base).unwrap();
    let root = tempfile::Builder::new()
        .prefix(&format!("mass-migrated_offline_{mode}-"))
        .tempdir_in(base)
        .unwrap()
        .keep();
    eprintln!("offline copy evidence: {}", root.display());
    let native = json!({"command":std::env::var("MASS_TEST_CODEX").unwrap(),"sha256":std::env::var("MASS_TEST_CODEX_SHA256").unwrap()});
    let config = if mode == "default" {
        None
    } else {
        Some(
            json!({"dataDir":if mode=="relative" {json!("data")} else {json!(root.join("data"))},"runtimes":[{"name":"codex","type":"codex","config":native}]}),
        )
    };
    if matches!(mode, "absolute" | "resume") {
        copy_fixture(&source.join("data"), &root.join("data"));
    }
    let mut server = TestServer::prepared(
        config,
        if mode == "relative" {
            Some("config-files")
        } else {
            None
        },
        |directory| {
            if mode == "default" {
                copy_fixture(&source.join("data"), &directory.join("data"));
            }
            if mode == "relative" {
                copy_fixture(&source.join("data"), &directory.join("config-files/data"));
            }
        },
    );
    let data = match mode {
        "default" => server._directory.path().join("data"),
        "relative" => server._directory.path().join("config-files/data"),
        _ => root.join("data"),
    };
    let copied = snapshot(&data);
    assert_eq!(
        copied, before,
        "copy must preserve every original metadata/history byte"
    );
    let journal = session_directory(&data, session).join("events.jsonl");
    let original = std::fs::read(&journal).unwrap();
    assert_eq!(server.rpc("status", json!({}))["connected"], 0);
    assert_eq!(
        server
            .rpc("session/list", json!({}))
            .as_array()
            .unwrap()
            .len(),
        1
    );
    let status = server.rpc("session/status", json!({"sessionName":"saved"}));
    assert_eq!(status["sessionId"], session["sessionId"]);
    assert_eq!(status["connected"], false);
    assert!(status["state"].is_null());
    assert!(status["configOptions"].is_null());
    assert_eq!(status["capabilities"]["prompt"], false);
    assert_eq!(
        server.rpc("session/get_event", json!({"sessionName":"saved"}))["events"],
        json!(expected)
    );
    assert_eq!(snapshot(&data), copied);
    if mode == "resume" {
        let resumed = server.rpc("session/resume", json!({"sessionName":"saved"}));
        assert_eq!(resumed["sessionId"], session["sessionId"]);
        assert_eq!(std::fs::read(&journal).unwrap(), original);
        let receipt=server.rpc("session/prompt",json!({"sessionName":"saved","prompt":[{"type":"text","text":"Without tools, reply only ROOT_MIGRATION_BIRCH_2719."}]}));
        assert_eq!(receipt["acceptance"], "native_recorded");
        let deadline = std::time::Instant::now() + Duration::from_secs(180);
        while server.rpc("session/status", json!({"sessionName":"saved"}))["state"] != "idle" {
            assert!(std::time::Instant::now() < deadline);
            std::thread::sleep(Duration::from_millis(100));
        }
        let result = server.rpc("session/get_event", json!({"sessionName":"saved"}));
        let continued = result["events"].as_array().unwrap();
        assert_eq!(&continued[..expected.len()], expected.as_slice());
        assert_eq!(
            continued[expected.len()]["seq"],
            expected.last().unwrap()["seq"].as_u64().unwrap() + 1
        );
        assert!(continued[expected.len()..].iter().any(|e| {
            e["type"] == "agent.message"
                && e["payload"]
                    .to_string()
                    .contains("ROOT_MIGRATION_BIRCH_2719")
        }));
        for (index, event) in continued.iter().enumerate() {
            assert_eq!(event["seq"], index + 1);
        }
        assert!(std::fs::read(&journal).unwrap().starts_with(&original));
        assert_eq!(
            mass_journal::history(&journal, 0, 1000).unwrap(),
            *continued
        );
        assert_eq!(
            server.rpc("session/archive", json!({"sessionName":"saved"}))["connected"],
            false
        );
    }
    server.stop();
    for file in ["http.jsonl", "cleanup.json"] {
        std::fs::copy(server._directory.path().join(file), root.join(file)).unwrap();
    }
    if matches!(mode, "default" | "relative") {
        copy_fixture(&data, &root.join("copied-data"));
    }
    assert_eq!(
        snapshot(&source.join("data")),
        before,
        "original source fixture must remain byte-identical"
    );
    let hashes: BTreeMap<_, _> = before
        .iter()
        .map(|(path, bytes)| {
            (
                path.to_string_lossy().into_owned(),
                format!("{:x}", Sha256::digest(bytes)),
            )
        })
        .collect();
    std::fs::write(root.join("result.json"),json!({"passed":true,"runner":"codex_text_offline.py","scenario":mode,"historyCount":expected.len(),"sourceDataSha256":hashes,"sourceEvidence":source}).to_string()).unwrap();
}
#[test]
#[ignore = "real native fixture required; copied absolute data path acceptance"]
fn migrated_offline_absolute() {
    offline_case("absolute");
}
#[test]
#[ignore = "real native fixture required; config-relative data from a different launch cwd"]
fn migrated_offline_relative() {
    offline_case("relative");
}
#[test]
#[ignore = "real native fixture required; cwd-relative no-config data acceptance"]
fn migrated_offline_default() {
    offline_case("default");
}
#[test]
#[ignore = "real native fixture and model required; explicit copied-data resume acceptance"]
fn migrated_offline_resume() {
    offline_case("resume");
}

#[test]
fn runtime_discovery_and_disabled_readiness_never_launch_an_agent() {
    use serde_json::json;
    use sha2::{Digest, Sha256};
    use std::os::unix::fs::PermissionsExt;
    let probe = tempfile::tempdir().unwrap();
    let command = probe.path().join("launch-probe");
    let marker = probe.path().join("starts");
    let script = b"#!/bin/sh\nprintf 'started\\n' >> \"$MASS_START_MARKER\"\nexit 1\n";
    std::fs::write(&command, script).unwrap();
    std::fs::set_permissions(&command, std::fs::Permissions::from_mode(0o755)).unwrap();
    let digest = format!("{:x}", Sha256::digest(script));
    for enabled in [true, false] {
        let mut server = TestServer::prepared(
            Some(
                json!({"dataDir":"data","runtimes":[{"name":"one","type":"codex","enabled":enabled,"config":{"command":command,"sha256":digest,"env":{"PRIVATE":"hidden-canary","MASS_START_MARKER":marker},"secretRefs":{"TOKEN":"env:PATH"}}}]}),
            ),
            None,
            |_| {},
        );
        assert_eq!(
            server.rpc("runtime/types", json!({})),
            json!([{"type":"codex"},{"type":"codex-remote"}])
        );
        let list = server.rpc("runtime/list", json!({}));
        assert_eq!(
            list,
            json!([{"name":"one","type":"codex","enabled":enabled,"status":{"phase":"pending","unavailableReason":null}}])
        );
        assert!(!list.to_string().contains("hidden-canary"));
        assert_eq!(server.rpc("runtime/status", json!({"name":"one"})), list[0]);
        let (status, body) = server.get("/readyz");
        assert_eq!(status, if enabled { 200 } else { 503 });
        assert_eq!(
            body["status"],
            if enabled { "ready" } else { "unavailable" }
        );
        assert_eq!(server.get("/livez").0, 200);
        for params in [
            json!({"name":"missing"}),
            json!({"name":"one","extra":true}),
        ] {
            let (_, error) = server.request(
                "POST",
                "/api/v1/agentd/jsonrpc",
                &json!({"jsonrpc":"2.0","id":1,"method":"agentd/runtime/status","params":params})
                    .to_string(),
            );
            assert_eq!(error["error"]["code"], -32602);
        }
        if !enabled {
            let (_,error)=server.request("POST","/api/v1/agentd/jsonrpc",&json!({"jsonrpc":"2.0","id":1,"method":"agentd/session/new","params":{"sessionName":"denied","spec":{"runtimeName":"one","cwd":server._directory.path()}}}).to_string());
            assert!(
                error["error"]["message"]
                    .as_str()
                    .unwrap()
                    .contains("disabled")
            );
        }
        assert_eq!(
            std::fs::read_dir(server._directory.path().join("data/sessions"))
                .unwrap()
                .count(),
            0
        );
        let table = process::table();
        assert!(
            process::descendants(&table, server.child.id())
                .keys()
                .copied()
                .collect::<Vec<_>>()
                == vec![server.child.id()],
            "discovery spawned a child process: {:?}",
            process::descendants(&table, server.child.id())
        );
        server.stop();
        let cleanup: serde_json::Value = serde_json::from_slice(
            &std::fs::read(server._directory.path().join("cleanup.json")).unwrap(),
        )
        .unwrap();
        assert_eq!(cleanup["remainingOwned"], json!([]));
        if let Some(evidence) = std::env::var_os("MASS_REGISTRATION_EVIDENCE") {
            let directory = std::path::PathBuf::from(evidence).join(if enabled {
                "enabled"
            } else {
                "disabled"
            });
            std::fs::create_dir_all(&directory).unwrap();
            for file in ["http.jsonl", "cleanup.json"] {
                std::fs::copy(server._directory.path().join(file), directory.join(file)).unwrap();
            }
            std::fs::write(
                directory.join("launch-count.json"),
                json!({"launchCount":0,"markerExists":marker.exists()}).to_string(),
            )
            .unwrap();
        }
        assert!(
            !marker.exists(),
            "registration/discovery attempted an Agent launch"
        );
    }
}

fn session_directory(data: &std::path::Path, session: &serde_json::Value) -> std::path::PathBuf {
    let ledger: serde_json::Value =
        serde_json::from_slice(&std::fs::read(data.join("runtime-registry.json")).unwrap())
            .unwrap();
    let instance = serde_json::json!({"identity":ledger["records"][session["spec"]["runtimeName"].as_str().unwrap()]["identity"]});
    mass_journal::session_directory(data, &instance, session["sessionId"].as_str().unwrap())
}
