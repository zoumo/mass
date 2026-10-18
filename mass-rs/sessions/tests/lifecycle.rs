//! Public Session service exercised with an adapter that has no native coordinates.
use mass_runtime::{
    AttachmentControl, OpenRequest, OpenedAttachment, Runtime, RuntimeFuture, SessionControl,
};
use mass_runtime::{CommandError, CommandErrorKind, InputCommand, InputReceipt};
use mass_sessions::Service;
use serde_json::{Value, json};
use std::{
    collections::BTreeMap,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicUsize, Ordering},
    },
    time::Duration,
};
use tokio::sync::{Semaphore, oneshot};

struct Adapter {
    id: String,
    opens: AtomicUsize,
    activations: AtomicUsize,
    active: AtomicBool,
    gate: Arc<Semaphore>,
    unknown: AtomicBool,
    close_gate: Option<Arc<Semaphore>>,
    handles: Mutex<Vec<Arc<Binding>>>,
    returned_id: Mutex<Option<String>>,
    recoveries: Mutex<Vec<String>>,
}
impl Adapter {
    fn new(id: &str) -> Arc<Self> {
        Arc::new(Self {
            id: id.into(),
            opens: AtomicUsize::new(0),
            activations: AtomicUsize::new(0),
            active: AtomicBool::new(false),
            gate: Arc::new(Semaphore::new(100)),
            unknown: AtomicBool::new(false),
            close_gate: None,
            handles: Mutex::new(Vec::new()),
            returned_id: Mutex::new(None),
            recoveries: Mutex::new(Vec::new()),
        })
    }
    async fn opened(self: Arc<Self>, request: OpenRequest) -> anyhow::Result<OpenedAttachment> {
        self.gate.acquire().await?.forget();
        if let Some((id, _)) = &request.recovery {
            self.recoveries.lock().unwrap().push(id.clone());
        }
        let id = self.returned_id.lock().unwrap().clone().unwrap_or_else(|| {
            request
                .recovery
                .as_ref()
                .map_or_else(|| self.id.clone(), |(id, _)| id.clone())
        });
        let binding = Arc::new(Binding {
            online: AtomicBool::new(true),
            closing: AtomicBool::new(false),
            started: AtomicBool::new(false),
            events: (request.events)(&id),
            owner: Mutex::new(Some(request.owner)),
            close_gate: self.close_gate.clone(),
        });
        self.handles.lock().unwrap().push(binding.clone());
        Ok(OpenedAttachment {
            session_id: id,
            recovery: json!({"opaque":"adapter-owned"}),
            control: binding,
        })
    }
}
struct Binding {
    online: AtomicBool,
    closing: AtomicBool,
    started: AtomicBool,
    events: Arc<tokio::sync::Mutex<dyn mass_runtime::RuntimeEvents>>,
    owner: Mutex<Option<Arc<dyn Send + Sync>>>,
    close_gate: Option<Arc<Semaphore>>,
}
impl SessionControl for Binding {
    fn begin(
        self: Arc<Self>,
        _: InputCommand,
    ) -> Result<oneshot::Receiver<Result<InputReceipt, CommandError>>, CommandError> {
        if !self.online() || self.closing() {
            return Err(CommandError::new(CommandErrorKind::Offline, "offline"));
        }
        let (tx, rx) = oneshot::channel();
        tx.send(Ok(InputReceipt {
            operation_id: "operation".into(),
            submission_id: "submission".into(),
            message_id: "message".into(),
            acceptance: mass_runtime::Acceptance::NativeRecorded,
        }))
        .unwrap();
        Ok(rx)
    }
    fn cancel(self: Arc<Self>) -> RuntimeFuture<'static, Result<(), CommandError>> {
        Box::pin(async { Ok(()) })
    }
}
impl Binding {
    async fn release(&self) {
        self.events.lock().await.detach();
        self.online.store(false, Ordering::Release);
        self.closing.store(false, Ordering::Release);
        self.owner.lock().unwrap().take();
    }
}
impl AttachmentControl for Binding {
    fn online(&self) -> bool {
        self.online.load(Ordering::Acquire)
    }
    fn closing(&self) -> bool {
        self.closing.load(Ordering::Acquire)
    }
    fn commit(&self) {
        self.owner.lock().unwrap().take();
    }
    fn close(self: Arc<Self>) -> RuntimeFuture<'static, anyhow::Result<()>> {
        Box::pin(async move {
            if !self.online() {
                return Ok(());
            }
            self.closing.store(true, Ordering::Release);
            if let Some(gate) = &self.close_gate {
                if !self.started.swap(true, Ordering::AcqRel) {
                    let gate = gate.clone();
                    let binding = self.clone();
                    tokio::spawn(async move {
                        gate.acquire().await.unwrap().forget();
                        binding.release().await;
                    });
                }
                anyhow::bail!("release has not been observed");
            }
            self.release().await;
            Ok(())
        })
    }
}
impl Runtime for Adapter {
    fn runtime_type(&self) -> &str {
        "test-agent"
    }
    fn fingerprint(&self) -> anyhow::Result<String> {
        Ok("a".repeat(64))
    }
    fn validate_cwd(&self, _: &std::path::Path) -> anyhow::Result<()> {
        Ok(())
    }
    fn validate_recovery(
        &self,
        id: &str,
        binding: &Value,
        _: &std::path::Path,
    ) -> anyhow::Result<()> {
        anyhow::ensure!(
            id == self.id && *binding == json!({"opaque":"adapter-owned"}),
            "invalid opaque binding"
        );
        Ok(())
    }
    fn status(&self) -> RuntimeFuture<'_, Value> {
        Box::pin(async {
            json!({"phase":if self.active.load(Ordering::Acquire) { "ready" } else { "pending" },"unavailableReason":null})
        })
    }
    fn open(
        self: Arc<Self>,
        request: OpenRequest,
    ) -> RuntimeFuture<'static, anyhow::Result<OpenedAttachment>> {
        Box::pin(async move {
            self.opens.fetch_add(1, Ordering::AcqRel);
            if !self.active.swap(true, Ordering::AcqRel) {
                self.activations.fetch_add(1, Ordering::AcqRel);
            }
            if self.unknown.swap(false, Ordering::AcqRel) {
                tokio::spawn(async move {
                    let opened = self.opened(request).await.unwrap();
                    opened.control.close().await.unwrap();
                });
                return Err(mass_runtime::OpenUnknown.into());
            }
            self.opened(request).await
        })
    }
    fn validate_history_record(&self) -> fn(&str, &Value, &Value) -> anyhow::Result<()> {
        |_, event, proof| {
            anyhow::ensure!(
                *proof
                    == json!({"revision":"test-v1","text":event["payload"]["content"][0]["text"]}),
                "invalid opaque proof"
            );
            Ok(())
        }
    }
    fn close_admission(&self) -> RuntimeFuture<'_, ()> {
        Box::pin(async {})
    }
    fn stop(&self) -> RuntimeFuture<'_, anyhow::Result<()>> {
        Box::pin(async move {
            let handles = self.handles.lock().unwrap().clone();
            for binding in handles {
                binding.release().await;
            }
            self.active.store(false, Ordering::Release);
            Ok(())
        })
    }
}
fn service(root: &std::path::Path, runtimes: &[(&str, Arc<Adapter>)]) -> Service {
    Service::new(
        root.into(),
        runtimes
            .iter()
            .map(|(name, adapter)| (name.to_string(), adapter.clone()))
            .collect::<BTreeMap<_, _>>(),
        vec![],
        Duration::from_secs(1),
    )
    .unwrap()
}
async fn new(
    service: &Service,
    name: &str,
    runtime: &str,
) -> Result<Value, mass_sessions::RpcError> {
    service
        .call(
            "agentd/session/new",
            json!({"sessionName":name,"spec":{"runtimeName":runtime,"cwd":"/adapter-owned"}}),
        )
        .await
}
async fn query(service: &Service, method: &str, name: &str) -> Value {
    service
        .call(
            &format!("agentd/session/{method}"),
            json!({"sessionName":name}),
        )
        .await
        .unwrap()
}
async fn wait(mut ready: impl FnMut() -> bool) {
    tokio::time::timeout(Duration::from_secs(3), async {
        while !ready() {
            tokio::task::yield_now().await;
        }
    })
    .await
    .unwrap();
}
#[tokio::test]
async fn real_opaque_identity_is_scoped_by_stable_instance_and_safe_on_disk() {
    let root = tempfile::tempdir().unwrap();
    let a = Adapter::new("../雪/../../same-id\0?");
    let b = Adapter::new(&a.id);
    let service = service(root.path(), &[("a", a.clone()), ("b", b.clone())]);
    let one = new(&service, "one", "a").await.unwrap();
    let two = new(&service, "two", "b").await.unwrap();
    assert_eq!(one["sessionId"], a.id);
    assert_eq!(one["sessionId"], two["sessionId"]);
    assert_ne!(one["operationId"], one["sessionId"]);
    assert_ne!(one["operationId"], two["operationId"]);
    for value in [&one, &two] {
        for field in ["nativeSessionId", "recovery", "instance", "runtime"] {
            assert!(value.get(field).is_none());
        }
    }
    let stored = mass_journal::load(root.path()).unwrap();
    assert_ne!(stored["one"].instance, stored["two"].instance);
    for metadata in stored.values() {
        let directory = mass_journal::directory(root.path(), metadata);
        assert_eq!(directory.parent().unwrap(), root.path().join("sessions"));
        assert!(directory.join("metadata.json").is_file());
    }
    assert_eq!(query(&service, "status", "one").await["sessionId"], a.id);
    query(&service, "archive", "one").await;
    assert_eq!(query(&service, "status", "two").await["connected"], true);
    let resumed = query(&service, "resume", "one").await;
    assert_eq!(resumed["sessionId"], one["sessionId"]);
    assert_eq!(a.activations.load(Ordering::Acquire), 1);
    assert_eq!(b.activations.load(Ordering::Acquire), 1);
    service.shutdown().await;
    assert!(a.handles.lock().unwrap().iter().all(|h| !h.online()));
    assert!(b.handles.lock().unwrap().iter().all(|h| !h.online()));
}
#[tokio::test]
async fn persistence_failure_releases_only_the_unpublished_attachment_and_preserves_foreign_files()
{
    let root = tempfile::tempdir().unwrap();
    let a = Adapter::new("same");
    let service = service(root.path(), &[("a", a.clone())]);
    new(&service, "peer", "a").await.unwrap();
    let original = mass_journal::load(root.path()).unwrap();
    let bytes = std::fs::read(
        mass_journal::directory(root.path(), &original["peer"]).join("metadata.json"),
    )
    .unwrap();
    let error = new(&service, "failed", "a").await.unwrap_err().response();
    assert_eq!(error["data"]["kind"], "persistence_failed");
    assert!(error["data"]["operationId"].is_string());
    assert_eq!(a.opens.load(Ordering::Acquire), 2);
    assert!(a.handles.lock().unwrap()[0].online());
    assert!(!a.handles.lock().unwrap()[1].online());
    assert_eq!(
        std::fs::read(
            mass_journal::directory(root.path(), &original["peer"]).join("metadata.json")
        )
        .unwrap(),
        bytes
    );
    assert_eq!(
        query(&service, "list", "").await.as_array().unwrap().len(),
        1
    );
    // Released failed opens allow another attempt, but never replace a committed pair.
    new(&service, "failed", "a").await.unwrap_err();
    assert_eq!(a.opens.load(Ordering::Acquire), 3);
    service.shutdown().await;
}
#[tokio::test]
async fn caller_disconnection_does_not_cancel_open_or_lose_its_owner() {
    let root = tempfile::tempdir().unwrap();
    let mut a = Adapter::new("agent-created");
    Arc::get_mut(&mut a).unwrap().gate = Arc::new(Semaphore::new(0));
    let service = service(root.path(), &[("a", a.clone())]);
    let observer = {
        let service = service.clone();
        tokio::spawn(async move { new(&service, "owned", "a").await })
    };
    wait(|| a.opens.load(Ordering::Acquire) == 1).await;
    observer.abort();
    let _ = observer.await;
    assert_eq!(
        new(&service, "owned", "a").await.unwrap_err().response()["code"],
        -32602
    );
    a.gate.add_permits(1);
    tokio::time::timeout(Duration::from_secs(3), async {
        loop {
            if query(&service, "list", "").await.as_array().unwrap().len() == 1 {
                break;
            }
            tokio::task::yield_now().await;
        }
    })
    .await
    .unwrap();
    assert_eq!(
        query(&service, "status", "owned").await["sessionId"],
        "agent-created"
    );
    assert_eq!(a.opens.load(Ordering::Acquire), 1);
    service.shutdown().await;
}
#[tokio::test]
async fn unknown_open_retains_name_until_the_original_late_success_is_released() {
    let root = tempfile::tempdir().unwrap();
    let mut a = Adapter::new("late-agent-id");
    Arc::get_mut(&mut a).unwrap().gate = Arc::new(Semaphore::new(0));
    a.unknown.store(true, Ordering::Release);
    let service = service(root.path(), &[("a", a.clone())]);
    let error = new(&service, "late", "a").await.unwrap_err().response();
    assert_eq!(error["data"]["kind"], "open_unknown");
    assert!(error["data"].get("sessionId").is_none());
    assert!(new(&service, "late", "a").await.is_err());
    assert_eq!(a.opens.load(Ordering::Acquire), 1);
    a.gate.add_permits(1);
    wait(|| {
        a.handles
            .lock()
            .unwrap()
            .first()
            .is_some_and(|h| !h.online())
    })
    .await;
    assert_eq!(query(&service, "list", "").await, json!([]));
    assert_eq!(
        std::fs::read_dir(root.path().join("sessions"))
            .unwrap()
            .count(),
        0
    );
    a.gate.add_permits(1);
    new(&service, "late", "a").await.unwrap();
    service.shutdown().await;
}
#[tokio::test]
async fn unknown_close_keeps_the_old_binding_and_late_release_allows_same_id_resume() {
    let root = tempfile::tempdir().unwrap();
    let mut a = Adapter::new("agent-id");
    let gate = Arc::new(Semaphore::new(0));
    Arc::get_mut(&mut a).unwrap().close_gate = Some(gate.clone());
    let service = service(root.path(), &[("a", a.clone())]);
    let first = new(&service, "one", "a").await.unwrap();
    let old = a.handles.lock().unwrap()[0].clone();
    let error = service
        .call("agentd/session/archive", json!({"sessionName":"one"}))
        .await
        .unwrap_err()
        .response();
    assert_eq!(error["data"]["kind"], "close_unknown");
    assert_eq!(
        query(&service, "status", "one").await["lifecycle"],
        "closing"
    );
    assert!(
        service
            .call("agentd/session/resume", json!({"sessionName":"one"}))
            .await
            .is_err()
    );
    assert!(
        old.clone()
            .begin(InputCommand {
                operation_id: "test".into(),
                blocks: vec![]
            })
            .is_err()
    );
    assert_eq!(a.opens.load(Ordering::Acquire), 1);
    gate.add_permits(1);
    wait(|| !old.online()).await;
    assert_eq!(
        query(&service, "status", "one").await["lifecycle"],
        "offline"
    );
    assert_eq!(
        query(&service, "resume", "one").await["sessionId"],
        first["sessionId"]
    );
    assert!(
        old.begin(InputCommand {
            operation_id: "test".into(),
            blocks: vec![]
        })
        .is_err()
    );
    assert!(
        a.handles.lock().unwrap()[1]
            .clone()
            .begin(InputCommand {
                operation_id: "test".into(),
                blocks: vec![]
            })
            .is_ok()
    );
    service.shutdown().await;
}

struct Ignore;
impl mass_events::handlers::EventHandler for Ignore {
    fn handle<'a>(
        &'a mut self,
        _: &'a mass_events::handlers::HandlerContext,
        _: &'a mass_events::handlers::ImmutableEvent,
    ) -> mass_events::handlers::HandlerFuture<'a> {
        Box::pin(async { Ok(()) })
    }
}
#[tokio::test]
async fn handlers_start_only_after_durable_metadata_and_failed_opens_never_activate_them() {
    use mass_events::handlers::{HandlerRegistration, Selection};
    let root = tempfile::tempdir().unwrap();
    let a = Adapter::new("real-id");
    let calls = Arc::new(AtomicUsize::new(0));
    let count = calls.clone();
    let directory = root.path().to_path_buf();
    let registration = HandlerRegistration::new(
        "commit-probe",
        8.try_into().unwrap(),
        Selection::Committed,
        move |identity| {
            let loaded = mass_journal::load(&directory).unwrap();
            assert_eq!(
                loaded[&identity.session_name].session_id,
                identity.session_id
            );
            count.fetch_add(1, Ordering::AcqRel);
            Box::new(Ignore)
        },
    );
    let service = Service::new(
        root.path().into(),
        BTreeMap::from([("a".into(), a.clone())]),
        vec![registration],
        Duration::from_secs(1),
    )
    .unwrap();
    new(&service, "committed", "a").await.unwrap();
    wait(|| calls.load(Ordering::Acquire) == 1).await;
    new(&service, "conflicting", "a").await.unwrap_err();
    service.shutdown().await;
    assert_eq!(calls.load(Ordering::Acquire), 1);
}

#[tokio::test]
async fn handlers_with_equal_agent_ids_are_isolated_by_instance() {
    use mass_events::handlers::{HandlerRegistration, Selection};
    let root = tempfile::tempdir().unwrap();
    let a = Adapter::new("equal");
    let b = Adapter::new("equal");
    let identities = Arc::new(Mutex::new(Vec::new()));
    let recorded = identities.clone();
    let registration = HandlerRegistration::new(
        "instance-probe",
        8.try_into().unwrap(),
        Selection::Committed,
        move |identity| {
            recorded.lock().unwrap().push((
                identity.runtime_instance_id.clone(),
                identity.session_id.clone(),
            ));
            Box::new(Ignore)
        },
    );
    let service = Service::new(
        root.path().into(),
        BTreeMap::from([("a".into(), a), ("b".into(), b)]),
        vec![registration],
        Duration::from_secs(1),
    )
    .unwrap();
    new(&service, "one", "a").await.unwrap();
    new(&service, "two", "b").await.unwrap();
    wait(|| identities.lock().unwrap().len() == 2).await;
    let pairs = identities.lock().unwrap().clone();
    assert_ne!(pairs[0].0, pairs[1].0);
    assert_eq!(pairs[0].1, pairs[1].1);
    query(&service, "archive", "one").await;
    query(&service, "resume", "one").await;
    service.shutdown().await;
    assert_eq!(
        identities.lock().unwrap().len(),
        2,
        "same pair resumes its original consumer without crossing instance ownership"
    );
}

async fn complete(adapter: &Adapter, text: &str) {
    let handle = adapter.handles.lock().unwrap().last().unwrap().clone();
    handle
        .events
        .lock()
        .await
        .submit(mass_runtime::RuntimeFact {
            event_type: "agent.message".into(),
            item_id: Some("opaque-item".into()),
            payload: json!({"messageId":"opaque-item","content":[{"type":"text","text":text}]}),
            evidence: json!({"revision":"test-v1","text":text}),
            transient: false,
        })
        .unwrap();
}

#[tokio::test]
async fn cold_opaque_recovery_continues_seq_without_replaying_old_subscriptions() {
    let root = tempfile::tempdir().unwrap();
    let a = Adapter::new("../雪/opaque\0id");
    let initial = service(root.path(), &[("a", a.clone())]);
    let created = new(&initial, "one", "a").await.unwrap();
    complete(&a, "before").await;
    let old = a.handles.lock().unwrap()[0].clone();
    initial.shutdown().await;
    let next = Adapter::new(&a.id);
    let restarted = service(root.path(), &[("a", next.clone())]);
    assert_eq!(next.opens.load(Ordering::Acquire), 0);
    let resumed = query(&restarted, "resume", "one").await;
    assert_eq!(created["sessionId"], resumed["sessionId"]);
    assert_eq!(*next.recoveries.lock().unwrap(), vec![a.id.clone()]);
    assert!(
        old.clone()
            .begin(InputCommand {
                operation_id: "old".into(),
                blocks: vec![]
            })
            .is_err()
    );
    let before = restarted
        .call("agentd/session/get_event", json!({"sessionName":"one"}))
        .await
        .unwrap();
    assert_eq!(before["events"].as_array().unwrap().len(), 1);
    complete(&next, "after").await;
    let after = restarted
        .call("agentd/session/get_event", json!({"sessionName":"one"}))
        .await
        .unwrap();
    assert_eq!(after["events"][0], before["events"][0]);
    assert_eq!(after["events"][1]["seq"], 2);
    restarted.shutdown().await;
}

#[tokio::test]
async fn every_committed_record_and_binding_is_checked_before_activation_or_tail_repair() {
    for mutation in [
        "seq",
        "id",
        "name",
        "schema",
        "extra",
        "proof",
        "projection",
        "binding",
        "cwd",
        "metadata",
    ] {
        let root = tempfile::tempdir().unwrap();
        let a = Adapter::new("opaque-agent");
        let initial = service(root.path(), &[("a", a.clone())]);
        new(&initial, "one", "a").await.unwrap();
        complete(&a, "first").await;
        complete(&a, "page-external").await;
        initial.shutdown().await;
        let metadata = mass_journal::load(root.path())
            .unwrap()
            .remove("one")
            .unwrap();
        let directory = mass_journal::directory(root.path(), &metadata);
        let path = directory.join("events.jsonl");
        let mut rows: Vec<Value> = std::fs::read_to_string(&path)
            .unwrap()
            .lines()
            .map(|l| serde_json::from_str(l).unwrap())
            .collect();
        let row = &mut rows[1];
        match mutation {
            "seq" => row["event"]["seq"] = json!(3),
            "id" => row["event"]["sessionId"] = json!("foreign"),
            "name" => row["event"]["sessionName"] = json!("foreign"),
            "schema" => row["event"]["schemaVersion"] = json!(99),
            "extra" => row["extra"] = json!(true),
            "proof" => row["evidence"]["revision"] = json!("unknown"),
            "projection" => row["event"]["payload"]["content"][0]["text"] = json!("forged"),
            _ => {}
        }
        let damaged = format!(
            "{}\n{{\"completeButUncommitted\":true}}",
            rows.iter()
                .map(Value::to_string)
                .collect::<Vec<_>>()
                .join("\n")
        );
        std::fs::write(&path, &damaged).unwrap();
        let next = Adapter::new(&a.id);
        let restarted = service(root.path(), &[("a", next.clone())]);
        if matches!(mutation, "binding" | "cwd" | "metadata") {
            let meta_path = directory.join("metadata.json");
            let mut bad = serde_json::to_value(&metadata).unwrap();
            match mutation {
                "binding" => bad["recovery"] = json!({"opaque":"changed"}),
                "cwd" => bad["spec"]["cwd"] = json!("/other"),
                _ => bad["formatVersion"] = json!(99),
            }
            std::fs::write(meta_path, bad.to_string()).unwrap();
        }
        assert!(
            restarted
                .call("agentd/session/resume", json!({"sessionName":"one"}))
                .await
                .is_err(),
            "{mutation}"
        );
        assert_eq!(next.opens.load(Ordering::Acquire), 0, "{mutation}");
        assert_eq!(
            std::fs::read_to_string(&path).unwrap(),
            damaged,
            "{mutation}"
        );
        if matches!(mutation, "seq" | "id" | "name" | "schema" | "extra") {
            assert!(
                restarted
                    .call(
                        "agentd/session/get_event",
                        json!({"sessionName":"one","limit":1})
                    )
                    .await
                    .is_err(),
                "{mutation}"
            );
        }
        restarted.shutdown().await;
    }
}

#[tokio::test]
async fn wrong_returned_identity_never_replaces_original_session_and_unknown_resume_keeps_owner() {
    let root = tempfile::tempdir().unwrap();
    let a = Adapter::new("saved-agent-id");
    let initial = service(root.path(), &[("a", a.clone())]);
    new(&initial, "one", "a").await.unwrap();
    query(&initial, "archive", "one").await;
    *a.returned_id.lock().unwrap() = Some("replacement-id".into());
    assert!(
        initial
            .call("agentd/session/resume", json!({"sessionName":"one"}))
            .await
            .is_err()
    );
    assert!(!a.handles.lock().unwrap().last().unwrap().online());
    assert_eq!(
        query(&initial, "status", "one").await["sessionId"],
        "saved-agent-id"
    );
    *a.returned_id.lock().unwrap() = None;
    a.gate.close();
    initial.shutdown().await;

    let next = Adapter::new("saved-agent-id");
    // Unknown resume remains owned until the original late attachment is released.
    let restarted = service(root.path(), &[("a", next.clone())]);
    next.gate.acquire_many(100).await.unwrap().forget();
    next.unknown.store(true, Ordering::Release);
    let error = restarted
        .call("agentd/session/resume", json!({"sessionName":"one"}))
        .await
        .unwrap_err()
        .response();
    assert_eq!(error["data"]["kind"], "open_unknown");
    assert!(
        restarted
            .call("agentd/session/resume", json!({"sessionName":"one"}))
            .await
            .is_err()
    );
    assert_eq!(next.opens.load(Ordering::Acquire), 1);
    next.gate.add_permits(1);
    wait(|| {
        next.handles
            .lock()
            .unwrap()
            .first()
            .is_some_and(|h| !h.online())
    })
    .await;
    next.gate.add_permits(1);
    assert_eq!(
        query(&restarted, "resume", "one").await["sessionId"],
        "saved-agent-id"
    );
    restarted.shutdown().await;
}

#[tokio::test]
async fn runtime_type_discovery_uses_the_composed_catalog() {
    let root = tempfile::tempdir().unwrap();
    let service = service(root.path(), &[]);
    assert_eq!(
        service
            .call("agentd/runtime/types", json!({}))
            .await
            .unwrap(),
        json!([])
    );
}

#[tokio::test]
async fn runtime_type_catalog_includes_unconfigured_composed_types() {
    let root = tempfile::tempdir().unwrap();
    let adapter = Adapter::new("opaque");
    let service = service(root.path(), &[("one", adapter.clone())]);
    assert_eq!(
        service
            .call("agentd/runtime/types", json!({}))
            .await
            .unwrap(),
        json!([{"type":"test-agent"}])
    );
    let service = service.with_runtime_types(vec![
        "future-agent".into(),
        "test-agent".into(),
        "future-agent".into(),
    ]);
    assert_eq!(
        service
            .call("agentd/runtime/types", json!({}))
            .await
            .unwrap(),
        json!([{"type":"future-agent"},{"type":"test-agent"}])
    );
    assert_eq!(adapter.activations.load(Ordering::Acquire), 0);
}
