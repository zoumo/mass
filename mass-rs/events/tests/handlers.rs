use mass_events::{Events, handlers::*};
use mass_runtime::{CommandError, CommandErrorKind, InputCommand, InputReceipt};
use mass_runtime::{RuntimeEvents, RuntimeFuture, SessionCommands, SessionControl};
use serde_json::{Value, json};
use std::sync::{
    Arc, Mutex,
    atomic::{AtomicUsize, Ordering},
};
use std::time::Duration;
use tokio::sync::oneshot;

struct Offline;
impl SessionControl for Offline {
    fn begin(
        self: Arc<Self>,
        _: InputCommand,
    ) -> Result<oneshot::Receiver<Result<InputReceipt, CommandError>>, CommandError> {
        Err(CommandError::new(CommandErrorKind::Offline, "offline"))
    }
    fn cancel(self: Arc<Self>) -> RuntimeFuture<'static, Result<(), CommandError>> {
        Box::pin(async { Err(CommandError::new(CommandErrorKind::Offline, "offline")) })
    }
}
fn context(connection_id: uuid::Uuid) -> HandlerContext {
    HandlerContext {
        session: SessionIdentity {
            session_name: "test".into(),
            session_id: "mass".into(),
            runtime_instance_id: "instance".into(),
            connection_id,
        },
        delivery: DeliveryKind::Transient,
        commands: SessionCommands::new(Arc::new(Offline)),
    }
}
struct Recorder {
    rows: Arc<Mutex<Vec<(Value, uuid::Uuid)>>>,
    closed: Arc<AtomicUsize>,
    fail: bool,
}
impl EventHandler for Recorder {
    fn handle<'a>(
        &'a mut self,
        ctx: &'a HandlerContext,
        event: &'a ImmutableEvent,
    ) -> HandlerFuture<'a> {
        Box::pin(async move {
            if self.fail {
                anyhow::bail!("extension failure");
            }
            assert_eq!(ctx.delivery, DeliveryKind::Committed);
            self.rows
                .lock()
                .unwrap()
                .push((event.value().clone(), ctx.session.connection_id));
            Ok(())
        })
    }
    fn close(&mut self) -> HandlerFuture<'_> {
        Box::pin(async {
            self.closed.fetch_add(1, Ordering::SeqCst);
            Ok(())
        })
    }
}

#[tokio::test]
async fn handler_failure_is_isolated_and_persistent_lane_uses_new_context_without_replay() {
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("events.jsonl");
    let rows = Arc::new(Mutex::new(Vec::new()));
    let closed = Arc::new(AtomicUsize::new(0));
    let factories = Arc::new(AtomicUsize::new(0));
    let registrations = [false, true]
        .into_iter()
        .map(|fail| {
            let rows = rows.clone();
            let closed = closed.clone();
            let factories = factories.clone();
            HandlerRegistration::new(
                format!("handler-{fail}"),
                8.try_into().unwrap(),
                Selection::Committed,
                move |_| {
                    factories.fetch_add(1, Ordering::SeqCst);
                    Box::new(Recorder {
                        rows: rows.clone(),
                        closed: closed.clone(),
                        fail,
                    })
                },
            )
        })
        .collect();
    let owner = HandlerTasks::new(registrations, Duration::from_secs(1));
    let mut core = Events::new("test".into(), "mass".into(), path.clone());
    let first = uuid::Uuid::now_v7();
    let second = uuid::Uuid::now_v7();
    core.activate(&owner, context(first));
    core.submit(mass_runtime::RuntimeFact {
        event_type: "agent.message".into(),
        item_id: Some("one".into()),
        payload: json!({"messageId":"one","content":[]}),
        evidence: json!({"version":1}),
        transient: true,
    })
    .unwrap();
    core.submit(mass_runtime::RuntimeFact {
        event_type: "agent.message".into(),
        item_id: Some("one".into()),
        payload: json!({"messageId":"one","content":[]}),
        evidence: json!({"version":1}),
        transient: false,
    })
    .unwrap();
    core.detach();
    core.activate(&owner, context(second));
    core.submit(mass_runtime::RuntimeFact {
        event_type: "agent.message".into(),
        item_id: Some("two".into()),
        payload: json!({"messageId":"two","content":[]}),
        evidence: json!({"version":1}),
        transient: false,
    })
    .unwrap();
    owner.shutdown().await;
    let rows = rows.lock().unwrap();
    assert_eq!(
        rows.iter()
            .map(|(value, _)| value.clone())
            .collect::<Vec<_>>(),
        mass_journal::history(&path, 0, 100).unwrap()
    );
    assert_eq!(
        rows.iter().map(|(_, id)| *id).collect::<Vec<_>>(),
        vec![first, second]
    );
    assert_eq!(
        factories.load(Ordering::SeqCst),
        2,
        "one instance per registration, not per attachment"
    );
    assert_eq!(closed.load(Ordering::SeqCst), 2);
}

struct Stalled(Arc<AtomicUsize>);
impl Drop for Stalled {
    fn drop(&mut self) {
        self.0.fetch_add(1, Ordering::SeqCst);
    }
}
impl EventHandler for Stalled {
    fn handle<'a>(&'a mut self, _: &'a HandlerContext, _: &'a ImmutableEvent) -> HandlerFuture<'a> {
        Box::pin(std::future::pending())
    }
}

#[tokio::test]
async fn stalled_cooperative_handler_is_aborted_and_dropped_at_shutdown_deadline() {
    let root = tempfile::tempdir().unwrap();
    let dropped = Arc::new(AtomicUsize::new(0));
    let counter = dropped.clone();
    let owner = HandlerTasks::new(
        vec![HandlerRegistration::new(
            "stall",
            1.try_into().unwrap(),
            Selection::All,
            move |_| Box::new(Stalled(counter.clone())),
        )],
        Duration::from_millis(20),
    );
    let mut core = Events::new(
        "test".into(),
        "mass".into(),
        root.path().join("events.jsonl"),
    );
    core.activate(&owner, context(uuid::Uuid::now_v7()));
    core.submit(mass_runtime::RuntimeFact {
        event_type: "session.state_update".into(),
        item_id: None,
        payload: json!({"state":"working"}),
        evidence: serde_json::Value::Null,
        transient: true,
    })
    .unwrap();
    tokio::task::yield_now().await;
    tokio::time::timeout(Duration::from_secs(1), owner.shutdown())
        .await
        .unwrap();
    assert_eq!(dropped.load(Ordering::SeqCst), 1);
}

#[tokio::test]
async fn handler_and_live_subscriber_receive_the_same_immutable_committed_object() {
    struct Probe(Option<oneshot::Sender<ImmutableEvent>>);
    impl EventHandler for Probe {
        fn handle<'a>(
            &'a mut self,
            _: &'a HandlerContext,
            event: &'a ImmutableEvent,
        ) -> HandlerFuture<'a> {
            let event = event.clone();
            Box::pin(async move {
                self.0.take().unwrap().send(event).unwrap();
                Ok(())
            })
        }
    }
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("events.jsonl");
    let (sender, receiver) = oneshot::channel();
    let sender = Mutex::new(Some(sender));
    let handlers = HandlerTasks::new(
        vec![HandlerRegistration::new(
            "identity",
            1.try_into().unwrap(),
            Selection::Committed,
            move |_| Box::new(Probe(sender.lock().unwrap().take())),
        )],
        Duration::from_secs(1),
    );
    let mut core = Events::new("test".into(), "mass".into(), path.clone());
    core.activate(&handlers, context(uuid::Uuid::now_v7()));
    let mut live = core.subscribe();
    core.submit(mass_runtime::RuntimeFact {
        event_type: "agent.message".into(),
        item_id: Some("non-codex-item".into()),
        payload: json!({"messageId":"non-codex-item","content":[]}),
        evidence: json!({"version":1,"proof":"private"}),
        transient: false,
    })
    .unwrap();
    let public = live.try_recv().unwrap();
    let handled = receiver.await.unwrap();
    assert!(public.same_object(&handled));
    assert_eq!(
        mass_journal::history(&path, 0, 1).unwrap(),
        vec![public.value().clone()]
    );
    assert!(!handled.value().to_string().contains("private"));
    handlers.shutdown().await;
}
