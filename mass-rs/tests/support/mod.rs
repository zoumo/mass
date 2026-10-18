pub(crate) mod process;
use futures_util::StreamExt;
use mass_events::handlers::*;
use mass_runtime::SessionCommands;
pub use process::table as processes;
use serde_json::{Value, json};
use std::{
    collections::BTreeMap,
    path::PathBuf,
    sync::{Arc, Mutex},
    time::Duration,
};

pub struct Native {
    pub service: Arc<mass_sessions::Service>,
    pub root: PathBuf,
    pub address: std::net::SocketAddr,
    client: reqwest::Client,
    server: Mutex<Option<tokio::task::JoinHandle<std::io::Result<()>>>>,
    transcript: Mutex<std::fs::File>,
    commands: Arc<Mutex<Option<SessionCommands>>>,
    rows: Arc<Mutex<Vec<Value>>>,
    observed: Arc<Mutex<BTreeMap<u32, Value>>>,
    runtime_evidence: Arc<Mutex<BTreeMap<String, Value>>>,
    transport_loss_cleanup: std::sync::atomic::AtomicBool,
}
struct Record {
    rows: Arc<Mutex<Vec<Value>>>,
    commands: Arc<Mutex<Option<SessionCommands>>>,
}
impl EventHandler for Record {
    fn handle<'a>(
        &'a mut self,
        ctx: &'a HandlerContext,
        event: &'a ImmutableEvent,
    ) -> HandlerFuture<'a> {
        Box::pin(async move {
            if ctx.session.session_name == "target" {
                let mut commands = self.commands.lock().unwrap();
                if commands.is_none() {
                    *commands = Some(ctx.commands.clone());
                }
            }
            self.rows.lock().unwrap().push(event.value().clone());
            Ok(())
        })
    }
}

impl Native {
    pub fn new(case: &str, observation: mass_codex::Observation) -> Self {
        Self::configured(case, observation, json!({}))
    }
    pub fn configured(case: &str, observation: mass_codex::Observation, overrides: Value) -> Self {
        Self::from_root(
            Self::fresh_root(case),
            observation,
            overrides,
            None,
            Vec::new(),
        )
    }
    /// Controlled tool-output fixtures must not include unrelated user startup diagnostics.
    pub fn with_shell_fixture(case: &str, observation: mass_codex::Observation) -> Self {
        let root = Self::fresh_root(case);
        let shell_config = root.join("shell-config");
        std::fs::create_dir(&shell_config).unwrap();
        let overrides = json!({"env":{"ZDOTDIR":shell_config}});
        std::fs::write(
            root.join("shell-fixture.json"),
            serde_json::to_vec_pretty(&overrides).unwrap(),
        )
        .unwrap();
        Self::from_root(root, observation, overrides, None, Vec::new())
    }
    pub fn with_handlers(
        case: &str,
        observation: mass_codex::Observation,
        overrides: Value,
        factory: impl FnOnce(&std::path::Path) -> Vec<HandlerRegistration>,
    ) -> Self {
        let root = Self::fresh_root(case);
        let extra = factory(&root);
        Self::from_root(root, observation, overrides, None, extra)
    }
    pub fn reopen(&self, observation: mass_codex::Observation, overrides: Value) -> Self {
        assert!(
            *self.service.stopped().borrow(),
            "one live Service must own each data directory"
        );
        Self::from_root(
            self.root.clone(),
            observation,
            overrides,
            Some(self),
            Vec::new(),
        )
    }
    fn fresh_root(case: &str) -> PathBuf {
        let base = std::env::var_os("MASS_TEST_EVIDENCE")
            .map(PathBuf::from)
            .unwrap_or_else(std::env::temp_dir);
        std::fs::create_dir_all(&base).unwrap();
        let root = tempfile::Builder::new()
            .prefix(&format!("mass-{case}-"))
            .tempdir_in(base)
            .unwrap()
            .keep();
        eprintln!("native evidence: {}", root.display());
        root
    }
    fn from_root(
        root: PathBuf,
        mut observation: mass_codex::Observation,
        overrides: Value,
        previous: Option<&Self>,
        extra: Vec<HandlerRegistration>,
    ) -> Self {
        let binary = std::env::var("MASS_TEST_CODEX")
            .expect("set MASS_TEST_CODEX to the reviewed absolute Codex 0.160.1 binary");
        let sha = std::env::var("MASS_TEST_CODEX_SHA256")
            .expect("set MASS_TEST_CODEX_SHA256 to its reviewed SHA256");
        observation.directory = Some(root.join("native"));
        for path in [
            &mut observation.thread_response_delay_cwd,
            &mut observation.core_write_delay_thread,
        ]
        .into_iter()
        .flatten()
        {
            if path.is_relative() {
                *path = root.join(&*path);
            }
        }
        let mut config = json!({"provider":"codex","executable":binary,"sha256":sha});
        config
            .as_object_mut()
            .unwrap()
            .extend(overrides.as_object().unwrap().clone());
        let config: mass_codex::Config = serde_json::from_value(config).unwrap();
        let other = Arc::new(mass_codex::ProviderRuntime::new(
            config.clone(),
            observation.clone(),
        ));
        let provider = Arc::new(mass_codex::ProviderRuntime::new(config, observation));
        let commands = previous
            .map(|h| h.commands.clone())
            .unwrap_or_else(|| Arc::new(Mutex::new(None)));
        let rows = previous
            .map(|h| h.rows.clone())
            .unwrap_or_else(|| Arc::new(Mutex::new(Vec::new())));
        let record_rows = rows.clone();
        let record_commands = commands.clone();
        let registration = HandlerRegistration::new(
            "recorder",
            256.try_into().unwrap(),
            Selection::Committed,
            move |_| {
                Box::new(Record {
                    rows: record_rows.clone(),
                    commands: record_commands.clone(),
                })
            },
        );
        let mut registrations = vec![registration];
        registrations.extend(extra);
        let service = Arc::new(
            mass_sessions::Service::new(
                root.join("data"),
                BTreeMap::from([("codex".into(), provider), ("other".into(), other)]),
                registrations,
                Duration::from_secs(3),
            )
            .unwrap(),
        );
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        listener.set_nonblocking(true).unwrap();
        let address = listener.local_addr().unwrap();
        let listener = tokio::net::TcpListener::from_std(listener).unwrap();
        let app = mass_http::router(service.clone());
        let mut stopped = service.stopped();
        let server = tokio::spawn(async move {
            axum::serve(listener, app)
                .with_graceful_shutdown(async move {
                    loop {
                        if *stopped.borrow() {
                            break;
                        }
                        if stopped.changed().await.is_err() {
                            break;
                        }
                    }
                })
                .await
        });
        let transcript = Mutex::new(
            std::fs::OpenOptions::new()
                .append(true)
                .create(true)
                .open(root.join("rpc.jsonl"))
                .unwrap(),
        );
        Self {
            service,
            root,
            address,
            client: reqwest::Client::builder()
                .no_proxy()
                .connect_timeout(Duration::from_secs(5))
                .timeout(Duration::from_secs(45))
                .build()
                .unwrap(),
            server: Mutex::new(Some(server)),
            transcript,
            commands,
            rows,
            observed: previous
                .map(|h| h.observed.clone())
                .unwrap_or_else(|| Arc::new(Mutex::new(BTreeMap::new()))),
            runtime_evidence: previous
                .map(|h| h.runtime_evidence.clone())
                .unwrap_or_default(),
            transport_loss_cleanup: std::sync::atomic::AtomicBool::new(false),
        }
    }
    pub async fn call(&self, method: &str, name: &str, mut params: Value) -> Value {
        let started = now_ns();
        params["sessionName"] = json!(name);
        let response=self.client.post(format!("http://{}/api/v1/agentd/jsonrpc",self.address))
            .json(&json!({"jsonrpc":"2.0","id":"native-test","method":format!("agentd/{method}"),"params":params})).send().await.expect("native HTTP RPC deadline/transport");
        assert_eq!(response.status(), 200);
        let bytes = response.bytes().await.unwrap();
        assert!(
            bytes.len() <= 128 * 1024 * 1024,
            "bounded test HTTP response"
        );
        let response: Value = serde_json::from_slice(&bytes).unwrap();
        if response["result"]["connected"] == true {
            assert!(response["result"].get("nativeSessionId").is_none());
            assert!(response["result"].get("recovery").is_none());
            assert!(response["result"].get("instance").is_none());
            assert!(response["result"].get("runtime").is_none());
            let runtime = self.runtime(&response["result"]);
            if let Some(operation) = response["result"]["operationId"].as_str() {
                self.runtime_evidence
                    .lock()
                    .unwrap()
                    .insert(operation.to_owned(), runtime.clone());
            }
            let pid = runtime["pid"].as_u64().unwrap();
            let table = processes();
            if let Some(row) = table.get(&(pid as u32)) {
                self.observed
                    .lock()
                    .unwrap()
                    .entry(pid as u32)
                    .or_insert_with(|| row.clone());
            }
        }
        use std::io::Write;
        let mut file = self.transcript.lock().unwrap();
        writeln!(
            file,
            "{}",
            json!({"method":method,"params":params,"response":response,"startedAtNs":started,"finishedAtNs":now_ns()})
        )
        .unwrap();
        response
    }
    pub async fn result(&self, method: &str, name: &str, params: Value) -> Value {
        let value = self.call(method, name, params).await;
        assert!(value.get("result").is_some(), "{method}: {value}");
        value["result"].clone()
    }
    pub async fn new_session(&self, name: &str) -> Value {
        self.new_session_on(name, "codex").await
    }
    pub async fn new_session_on(&self, name: &str, runtime: &str) -> Value {
        let cwd = self.root.join(name);
        std::fs::create_dir_all(&cwd).unwrap();
        self.result(
            "session/new",
            name,
            json!({"spec":{"runtimeName":runtime,"cwd":cwd}}),
        )
        .await
    }
    pub async fn prompt(&self, name: &str, text: &str) -> Value {
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
    pub async fn idle(&self, name: &str) {
        self.idle_with(name, 180).await;
    }
    pub async fn idle_with(&self, name: &str, seconds: u64) {
        tokio::time::timeout(Duration::from_secs(seconds), async {
            loop {
                let status = self.result("session/status", name, json!({})).await;
                assert_eq!(
                    status["connected"], true,
                    "attachment disconnected before terminal: {status}"
                );
                if status["state"] == "idle" {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(100)).await;
            }
        })
        .await
        .expect("real native terminal deadline");
    }
    pub async fn history(&self, name: &str) -> Vec<Value> {
        // After daemon shutdown the verifier uses the public read-only Service seam;
        // it does not pretend the closed HTTP listener is still available.
        if *self.service.stopped().borrow() {
            return self
                .service
                .call(
                    "agentd/session/get_event",
                    json!({"sessionName":name,"limit":1000}),
                )
                .await
                .unwrap()["events"]
                .as_array()
                .unwrap()
                .clone();
        }
        self.result("session/get_event", name, json!({"limit":1000}))
            .await["events"]
            .as_array()
            .unwrap()
            .clone()
    }
    pub fn handler_rows(&self) -> Vec<Value> {
        self.rows.lock().unwrap().clone()
    }
    pub fn captured_commands(&self) -> SessionCommands {
        self.commands
            .lock()
            .unwrap()
            .clone()
            .expect("handler captured old command context")
    }
    pub fn session_dir(&self, session: &Value) -> PathBuf {
        let ledger: Value = serde_json::from_slice(
            &std::fs::read(self.root.join("data/runtime-registry.json")).unwrap(),
        )
        .unwrap();
        let instance = json!({"identity":ledger["records"][session["spec"]["runtimeName"].as_str().unwrap()]["identity"]});
        mass_journal::session_directory(
            &self.root.join("data"),
            &instance,
            session["sessionId"].as_str().unwrap(),
        )
    }
    /// Test-only evidence comes from the adapter recorder, never a public Session field.
    pub fn runtime(&self, session: &Value) -> Value {
        if let Some(operation) = session["operationId"].as_str()
            && let Some(runtime) = self.runtime_evidence.lock().unwrap().get(operation)
        {
            return runtime.clone();
        }
        let mut candidates = Vec::new();
        for entry in std::fs::read_dir(self.root.join("native")).unwrap() {
            let path = entry.unwrap().path();
            let Ok(wire) = std::fs::read_to_string(path.join("wire.jsonl")) else {
                continue;
            };
            for line in wire
                .split_inclusive('\n')
                .filter(|line| line.ends_with('\n'))
            {
                let row: Value = serde_json::from_str(line).unwrap();
                if row["direction"] == "server"
                    && row["payload"]["result"]["thread"]["id"] == session["sessionId"]
                {
                    let spawn: Value =
                        serde_json::from_slice(&std::fs::read(path.join("spawn.json")).unwrap())
                            .unwrap();
                    candidates.push((
                        row["time"].as_str().unwrap().parse::<i128>().unwrap(),
                        spawn["runtime"].clone(),
                    ));
                }
            }
        }
        candidates
            .into_iter()
            .max_by_key(|(time, _)| *time)
            .expect("Agent ID has real open evidence")
            .1
    }
    pub fn wire(&self) -> Value {
        let mut rows = Vec::new();
        if let Ok(entries) = std::fs::read_dir(self.root.join("native")) {
            let mut paths: Vec<_> = entries
                .map(|entry| entry.unwrap().path().join("wire.jsonl"))
                .collect();
            paths.sort();
            for path in paths {
                if let Ok(text) = std::fs::read_to_string(path) {
                    for line in text.split_inclusive('\n').filter(|s| s.ends_with('\n')) {
                        rows.push(serde_json::from_str::<Value>(line).unwrap());
                    }
                }
            }
        }
        json!(rows)
    }
    pub async fn wait_wire(&self, method: &str) -> Value {
        tokio::time::timeout(Duration::from_secs(90), async {
            loop {
                if let Some(row) = self
                    .wire()
                    .as_array()
                    .unwrap()
                    .iter()
                    .rev()
                    .find(|r| r["payload"]["method"] == method)
                {
                    return row.clone();
                }
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
        })
        .await
        .expect("actual native observation deadline")
    }
    pub async fn offline(&self, name: &str) {
        tokio::time::timeout(Duration::from_secs(30), async {
            loop {
                if self.result("session/status", name, json!({})).await["connected"] == false {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
        })
        .await
        .expect("native disconnect deadline");
    }
    pub async fn released(&self, name: &str) {
        tokio::time::timeout(Duration::from_secs(30), async {
            loop {
                if self.result("session/status", name, json!({})).await["lifecycle"] == "offline" {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
        })
        .await
        .expect("actual native release and core drain must settle before replacement");
    }
    /// Test-only read of private adapter proof; never enrich a public response.
    pub fn private_payload(&self, event: &Value) -> Value {
        let metadata = self.service_metadata(event["sessionName"].as_str().unwrap());
        let path = self.session_dir(&metadata).join("events.jsonl");
        let record = mass_journal::records(&path, event["seq"].as_u64().unwrap() - 1, 1)
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
    }
    fn service_metadata(&self, name: &str) -> Value {
        let data = mass_journal::load(&self.root.join("data")).unwrap();
        serde_json::to_value(&data[name]).unwrap()
    }
    pub async fn assert_history(&self, name: &str) {
        let history = self.history(name).await;
        let status = self
            .service
            .call("agentd/session/status", json!({"sessionName":name}))
            .await
            .unwrap();
        let path = self.session_dir(&status).join("events.jsonl");
        assert_eq!(mass_journal::history(&path, 0, 1000).unwrap(), history);
        tokio::time::timeout(Duration::from_secs(5), async {
            loop {
                let recorded: Vec<_> = self
                    .rows
                    .lock()
                    .unwrap()
                    .iter()
                    .filter(|r| r["sessionName"] == name)
                    .cloned()
                    .collect();
                if recorded == history {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(10)).await;
            }
        })
        .await
        .expect("Handler must observe identical committed records");
        let wire = self.wire();
        for (index, event) in history.iter().enumerate() {
            assert_eq!(event["seq"], index + 1);
            let private = self.private_payload(event);
            let payload = &private;
            assert_eq!(event["schemaVersion"], 2);
            for field in ["source", "nativeItem", "nativeResult", "nativeStatus"] {
                assert!(event["payload"].get(field).is_none());
            }
            mass_codex::validate_history_payload(event["type"].as_str().unwrap(), payload).unwrap();
            let item = &payload[if event["type"] == "agent.tool_result" {
                "nativeResult"
            } else {
                "nativeItem"
            }];
            let method = if event["type"] == "agent.tool_call" {
                "item/started"
            } else {
                "item/completed"
            };
            assert!(
                wire.as_array()
                    .unwrap()
                    .iter()
                    .any(|r| r["direction"] == "server"
                        && r["payload"]["method"] == method
                        && r["payload"]["params"]["threadId"] == payload["source"]["threadId"]
                        && r["payload"]["params"]["item"] == *item),
                "missing actual SDK fact for {event}"
            );
        }
    }
    pub async fn shutdown(&self) {
        if *self.service.stopped().borrow() {
            return;
        }
        let before = processes();
        let mut owned = self.observed.lock().unwrap().clone();
        let mut native_roots = std::collections::BTreeSet::new();
        if let Ok(entries) = std::fs::read_dir(self.root.join("native")) {
            for entry in entries {
                let path = entry.unwrap().path().join("spawn.json");
                if let Ok(bytes) = std::fs::read(path) {
                    let value: Value = serde_json::from_slice(&bytes).unwrap();
                    let pid = value["runtime"]["pid"].as_u64().unwrap() as u32;
                    native_roots.insert(pid);
                    if let Some(identity) = before.get(&pid)
                        && owned
                            .get(&pid)
                            .is_some_and(|known| known["start"] == identity["start"])
                    {
                        owned.insert(pid, identity.clone());
                    }
                }
            }
        }
        loop {
            let added: Vec<_> = before
                .iter()
                .filter(|(pid, row)| {
                    let parent = row["ppid"].as_u64().unwrap() as u32;
                    owned
                        .get(pid)
                        .is_none_or(|known| known["start"] != row["start"])
                        && owned.get(&parent).is_some_and(|known| {
                            before
                                .get(&parent)
                                .is_some_and(|now| now["start"] == known["start"])
                        })
                })
                .map(|(pid, row)| (*pid, row.clone()))
                .collect();
            if added.is_empty() {
                break;
            }
            owned.extend(added);
        }
        self.observed.lock().unwrap().extend(owned.clone());
        let wire = self.wire();
        std::fs::write(self.root.join("wire-final.json"), wire.to_string()).unwrap();
        let stopped = tokio::time::timeout(Duration::from_secs(40), self.service.shutdown())
            .await
            .is_ok();
        std::fs::write(
            self.root.join("handlers.json"),
            serde_json::to_vec_pretty(&*self.rows.lock().unwrap()).unwrap(),
        )
        .unwrap();
        let server = { self.server.lock().unwrap().take() };
        let server_stopped = if let Some(mut server) = server {
            match tokio::time::timeout(Duration::from_secs(5), &mut server).await {
                Ok(result) => result.is_ok_and(|r| r.is_ok()),
                Err(_) => {
                    server.abort();
                    let _ = server.await;
                    false
                }
            }
        } else {
            true
        };
        let native_reaped = std::fs::read_dir(self.root.join("native"))
            .ok()
            .into_iter()
            .flatten()
            .all(|entry| {
                let path = entry.unwrap().path();
                !path.join("spawn.json").exists()
                    || std::fs::read(path.join("cleanup.json"))
                        .ok()
                        .and_then(|bytes| serde_json::from_slice::<Value>(&bytes).ok())
                        .is_some_and(|value| value["reaped"] == true)
            });
        // External native helper shutdown can finish just after the owned host
        // is reaped. Allow bounded observation only; do not signal during this grace.
        let settle = tokio::time::Instant::now();
        let after = loop {
            let table = processes();
            if !owned.iter().any(|(pid, row)| {
                table
                    .get(pid)
                    .is_some_and(|now| now["start"] == row["start"])
            }) || settle.elapsed() >= Duration::from_secs(5)
            {
                break table;
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        };
        let original_remaining: Vec<_> = owned
            .iter()
            .filter(|(pid, row)| {
                after
                    .get(pid)
                    .is_some_and(|now| now["start"] == row["start"])
            })
            .map(|(_, row)| row.clone())
            .collect();
        let mut actions = Vec::new();
        for signal in ["-TERM", "-KILL"] {
            let table = processes();
            for (pid, row) in &owned {
                if table
                    .get(pid)
                    .is_some_and(|now| now["start"] == row["start"])
                {
                    let status = std::process::Command::new("/bin/kill")
                        .args([signal, &pid.to_string()])
                        .status()
                        .unwrap();
                    actions.push(json!({"identity":row,"signal":signal,"sent":status.success(),"protocolInterruptProof":false}));
                }
            }
            if !actions.is_empty() {
                tokio::time::sleep(Duration::from_millis(300)).await;
            }
        }
        let after = processes();
        let remaining: Vec<_> = owned
            .iter()
            .filter(|(pid, row)| {
                after
                    .get(pid)
                    .is_some_and(|now| now["start"] == row["start"])
            })
            .map(|(_, row)| row.clone())
            .collect();
        std::fs::write(self.root.join("cleanup.json"),serde_json::to_vec_pretty(&json!({"ownedBeforeShutdown":owned.values().collect::<Vec<_>>(),"remainingAfterProductStop":original_remaining,"harnessActions":actions,"remainingOwned":remaining,"productStopped":stopped,"httpStopped":server_stopped,"descendantSettleMs":settle.elapsed().as_millis()})).unwrap()).unwrap();
        assert!(
            remaining.is_empty(),
            "owned native PID/start identities remain: {remaining:?}"
        );
        assert!(stopped, "owned native shutdown deadline");
        assert!(
            native_reaped,
            "every spawned host must record an awaited child reaping, including failed provisional creation"
        );
        assert!(
            server_stopped,
            "HTTP server must finish/close its isolated listener"
        );
        assert!(
            !original_remaining
                .iter()
                .any(|row| native_roots.contains(&(row["pid"].as_u64().unwrap() as u32))),
            "product must reap its native host before any descendant-only harness cleanup"
        );
        assert!(
            original_remaining.is_empty()
                || self
                    .transport_loss_cleanup
                    .load(std::sync::atomic::Ordering::Relaxed),
            "product Stop left descendants; explicit harness cleanup was required: {original_remaining:?}"
        );
    }
}

pub struct Live {
    rows: Arc<Mutex<Vec<Value>>>,
    errors: Arc<Mutex<Option<String>>>,
    observations: Arc<Mutex<Vec<Value>>>,
    tasks: Vec<tokio::task::JoinHandle<()>>,
}
impl Live {
    pub fn observations(&self) -> Vec<Value> {
        self.observations.lock().unwrap().clone()
    }
    pub fn rows(&self) -> Vec<Value> {
        let error = self.errors.lock().unwrap().clone();
        assert!(error.is_none(), "SSE consumer failed: {error:?}");
        self.rows.lock().unwrap().clone()
    }
}
impl Drop for Live {
    fn drop(&mut self) {
        for task in &self.tasks {
            task.abort();
        }
    }
}
impl Native {
    pub async fn live(&self, name: &str) -> Live {
        let mut live = Live {
            rows: Arc::new(Mutex::new(Vec::new())),
            errors: Arc::new(Mutex::new(None)),
            observations: Arc::new(Mutex::new(Vec::new())),
            tasks: Vec::new(),
        };
        self.reconnect(&mut live, name).await;
        live
    }
    pub async fn reconnect(&self, live: &mut Live, name: &str) {
        let response=self.client.post(format!("http://{}/api/v1/agentd/jsonrpc",self.address)).timeout(Duration::from_secs(600))
            .json(&json!({"jsonrpc":"2.0","id":"watch","method":"agentd/session/watch_event","params":{"sessionName":name}})).send().await.unwrap();
        assert_eq!(response.headers()["content-type"], "text/event-stream");
        let mut stream = response.bytes_stream();
        let rows = live.rows.clone();
        let errors = live.errors.clone();
        let observations = live.observations.clone();
        live.tasks.push(tokio::spawn(async move {
            let mut pending = Vec::new();
            while let Some(chunk) = stream.next().await {
                let chunk = match chunk {
                    Ok(chunk) => chunk,
                    Err(error) => {
                        *errors.lock().unwrap() = Some(error.to_string());
                        break;
                    }
                };
                pending.extend_from_slice(&chunk);
                while let Some(end) = pending.windows(2).position(|bytes| bytes == b"\n\n") {
                    let frame: Vec<_> = pending.drain(..end + 2).collect();
                    for line in frame.split(|byte| *byte == b'\n') {
                        if let Some(data) = line.strip_prefix(b"data: ") {
                            match serde_json::from_slice::<Value>(data) {
                                Ok(value) => {
                                    observations
                                        .lock()
                                        .unwrap()
                                        .push(json!({"receivedAtNs":now_ns(),"event":value}));
                                    rows.lock().unwrap().push(value);
                                }
                                Err(error) => {
                                    *errors.lock().unwrap() = Some(error.to_string());
                                    return;
                                }
                            }
                        }
                    }
                }
            }
        }));
    }
    pub fn frames(&self, method: &str) -> Vec<Value> {
        self.wire()
            .as_array()
            .unwrap()
            .iter()
            .filter(|r| r["payload"]["method"] == method)
            .map(|r| r["payload"].clone())
            .collect()
    }
    pub async fn wait<T>(&self, mut predicate: impl FnMut() -> Option<T>) -> T {
        tokio::time::timeout(Duration::from_secs(150), async {
            loop {
                if let Some(value) = predicate() {
                    break value;
                }
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
        })
        .await
        .expect("scenario observation deadline")
    }
    pub fn owned_processes(&self, root: u32) -> BTreeMap<u32, Value> {
        let table = processes();
        let known = self
            .observed
            .lock()
            .unwrap()
            .get(&root)
            .cloned()
            .expect("native root identity captured at successful attachment");
        assert_eq!(
            table[&root]["start"], known["start"],
            "never adopt a reused native PID"
        );
        let owned = process::descendants(&table, root);
        self.observed.lock().unwrap().extend(owned.clone());
        owned
    }
    pub fn allow_captured_descendant_cleanup(&self) {
        self.transport_loss_cleanup
            .store(true, std::sync::atomic::Ordering::Relaxed);
    }
    pub fn expect_transport_loss_cleanup(&self) {
        self.transport_loss_cleanup
            .store(true, std::sync::atomic::Ordering::Relaxed);
    }
}
impl Native {
    pub async fn kill_host(&self, name: &str) {
        let status = self.result("session/status", name, json!({})).await;
        let pid = self.runtime(&status)["pid"].as_u64().unwrap() as u32;
        let owned = std::fs::read_dir(self.root.join("native"))
            .unwrap()
            .any(|entry| {
                std::fs::read(entry.unwrap().path().join("spawn.json"))
                    .ok()
                    .is_some_and(|bytes| {
                        serde_json::from_slice::<Value>(&bytes).unwrap()["runtime"]["pid"] == pid
                    })
            });
        assert!(
            owned,
            "only an observed test-owned host may receive fault signals"
        );
        let identities = self.owned_processes(pid);
        let identity = &identities[&pid];
        assert_eq!(processes()[&pid]["start"], identity["start"]);
        assert!(
            std::process::Command::new("/bin/kill")
                .args(["-KILL", &pid.to_string()])
                .status()
                .unwrap()
                .success()
        );
        std::fs::write(
            self.root.join("host-loss-trigger.json"),
            serde_json::to_vec_pretty(&json!({"identity":identity,"owned":identities})).unwrap(),
        )
        .unwrap();
    }
}

pub fn now_ns() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos()
        .try_into()
        .unwrap()
}
