//! The transport and Handler command handle share a coordinate-free MASS adapter.
use axum::{Json, body::to_bytes, extract::State};
use mass_runtime::{
    Acceptance, AcceptanceEvidence, AttachmentControl, CommandError, CommandErrorKind,
    InputCommand, InputReceipt, OpenRequest, OpenedAttachment, Runtime, RuntimeFuture,
    SessionCommands, SessionControl,
};
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
use tokio::sync::oneshot;

struct Binding {
    online: AtomicBool,
    calls: AtomicUsize,
    failure: Mutex<Option<CommandError>>,
    events: Arc<tokio::sync::Mutex<dyn mass_runtime::RuntimeEvents>>,
}
impl SessionControl for Binding {
    fn begin(
        self: Arc<Self>,
        command: InputCommand,
    ) -> Result<oneshot::Receiver<Result<InputReceipt, CommandError>>, CommandError> {
        if !self.online() {
            return Err(CommandError::new(
                CommandErrorKind::Offline,
                "attachment is offline",
            ));
        }
        self.calls.fetch_add(1, Ordering::SeqCst);
        let (tx, rx) = oneshot::channel();
        let result = if let Some(error) = self.failure.lock().unwrap().clone() {
            Err(error)
        } else {
            Ok(InputReceipt {
                operation_id: command.operation_id,
                submission_id: "MASS-submission".into(),
                message_id: "MASS-user-item".into(),
                acceptance: Acceptance::NativeRecorded,
            })
        };
        tx.send(result).unwrap();
        Ok(rx)
    }
    fn cancel(self: Arc<Self>) -> RuntimeFuture<'static, Result<(), CommandError>> {
        Box::pin(async move {
            self.calls.fetch_add(1, Ordering::SeqCst);
            self.failure.lock().unwrap().clone().map_or(Ok(()), Err)
        })
    }
}
impl AttachmentControl for Binding {
    fn online(&self) -> bool {
        self.online.load(Ordering::Acquire)
    }
    fn closing(&self) -> bool {
        false
    }
    fn commit(&self) {}
    fn close(self: Arc<Self>) -> RuntimeFuture<'static, anyhow::Result<()>> {
        Box::pin(async move {
            self.online.store(false, Ordering::Release);
            Ok(())
        })
    }
}
struct Adapter {
    bindings: Mutex<Vec<Arc<Binding>>>,
}
impl Runtime for Adapter {
    fn runtime_type(&self) -> &str {
        "coordinate-free"
    }
    fn fingerprint(&self) -> anyhow::Result<String> {
        Ok("d".repeat(64))
    }
    fn validate_cwd(&self, _: &std::path::Path) -> anyhow::Result<()> {
        Ok(())
    }
    fn validate_recovery(&self, _: &str, _: &Value, _: &std::path::Path) -> anyhow::Result<()> {
        Ok(())
    }
    fn validate_history_record(&self) -> fn(&str, &Value, &Value) -> anyhow::Result<()> {
        |_, _, _| Ok(())
    }
    fn status(&self) -> RuntimeFuture<'_, Value> {
        Box::pin(async { json!({"phase":"pending","unavailableReason":null}) })
    }
    fn open(
        self: Arc<Self>,
        request: OpenRequest,
    ) -> RuntimeFuture<'static, anyhow::Result<OpenedAttachment>> {
        Box::pin(async move {
            let session_id = request
                .recovery
                .as_ref()
                .map_or_else(|| "Agent ID / 不透明".into(), |(id, _)| id.clone());
            let events = (request.events)(&session_id);
            let binding = Arc::new(Binding {
                online: AtomicBool::new(true),
                calls: AtomicUsize::new(0),
                failure: Mutex::new(None),
                events,
            });
            self.bindings.lock().unwrap().push(binding.clone());
            Ok(OpenedAttachment {
                session_id,
                recovery: json!({"opaque":true}),
                control: binding,
            })
        })
    }
    fn close_admission(&self) -> RuntimeFuture<'_, ()> {
        Box::pin(async {})
    }
    fn stop(&self) -> RuntimeFuture<'_, anyhow::Result<()>> {
        Box::pin(async { Ok(()) })
    }
}
async fn call(service: &Arc<Service>, method: &str, params: Value) -> Value {
    let response = mass_http::rpc(
        State(service.clone()),
        Json(json!({"jsonrpc":"2.0","id":"request","method":method,"params":params})),
    )
    .await;
    serde_json::from_slice(
        &to_bytes(response.into_body(), 2 * 1024 * 1024)
            .await
            .unwrap(),
    )
    .unwrap()
}
fn without_operation(mut value: Value) -> Value {
    assert!(value["operationId"].is_string());
    value.as_object_mut().unwrap().remove("operationId");
    value
}
#[tokio::test]
async fn http_and_handler_commands_share_receipts_errors_validation_and_attachment_binding() {
    let root = tempfile::tempdir().unwrap();
    let adapter = Arc::new(Adapter {
        bindings: Mutex::new(vec![]),
    });
    let service = Arc::new(
        Service::new(
            root.path().into(),
            BTreeMap::from([("fixture".into(), adapter.clone())]),
            vec![],
            Duration::from_secs(1),
        )
        .unwrap(),
    );
    let created = call(
        &service,
        "agentd/session/new",
        json!({"sessionName":"test","spec":{"runtimeName":"fixture","cwd":"/adapter/owned"}}),
    )
    .await;
    assert_eq!(created["result"]["sessionId"], "Agent ID / 不透明");
    let binding = adapter.bindings.lock().unwrap()[0].clone();
    let handler_commands = SessionCommands::new(binding.clone());
    let blocks = vec![json!({"type":"text","text":"MASS input"})];
    let params = json!({"sessionName":"test","prompt":blocks});
    let http = call(&service, "agentd/session/prompt", params.clone()).await;
    let handler =
        serde_json::to_value(handler_commands.prompt(blocks.clone()).await.unwrap()).unwrap();
    assert_ne!(http["result"]["operationId"], handler["operationId"]);
    assert_eq!(
        without_operation(http["result"].clone()),
        without_operation(handler)
    );
    for kind in [
        CommandErrorKind::Busy,
        CommandErrorKind::Offline,
        CommandErrorKind::Rejected,
        CommandErrorKind::AcceptanceUnknown,
    ] {
        let mut failure = CommandError::new(kind, "MASS domain failure");
        failure.evidence = Some(AcceptanceEvidence {
            sent: true,
            native_accepted: false,
            native_recorded: true,
        });
        *binding.failure.lock().unwrap() = Some(failure);
        let http = call(&service, "agentd/session/prompt", params.clone()).await;
        let handler = handler_commands.prompt(blocks.clone()).await.unwrap_err();
        assert_eq!(http["error"]["data"]["kind"], kind.as_str());
        assert_ne!(
            http["error"]["data"]["operationId"],
            serde_json::to_value(&handler).unwrap()["operationId"]
        );
        assert_eq!(
            without_operation(http["error"]["data"].clone()),
            without_operation(serde_json::to_value(handler).unwrap())
        );
        assert_eq!(
            http["error"]["code"],
            if matches!(kind, CommandErrorKind::Busy | CommandErrorKind::Offline) {
                -32602
            } else {
                -32603
            }
        );
    }
    *binding.failure.lock().unwrap() = Some(CommandError::new(
        CommandErrorKind::CancelUnknown,
        "cancel outcome is unknown",
    ));
    let http = call(
        &service,
        "agentd/session/cancel",
        json!({"sessionName":"test"}),
    )
    .await;
    let handler = handler_commands.cancel().await.unwrap_err();
    assert_eq!(
        without_operation(http["error"]["data"].clone()),
        without_operation(serde_json::to_value(handler).unwrap())
    );
    let calls = binding.calls.load(Ordering::SeqCst);
    for blocks in [
        vec![],
        vec![json!({"type":"text","text":""})],
        vec![json!({"type":"image","data":"x"})],
        vec![json!({"type":"text","text":"x".repeat(1024*1024)})],
    ] {
        let http = call(
            &service,
            "agentd/session/prompt",
            json!({"sessionName":"test","prompt":blocks}),
        )
        .await;
        let handler = handler_commands.prompt(blocks).await.unwrap_err();
        assert_eq!(http["error"]["data"]["kind"], "invalid_input");
        assert_eq!(handler.kind, CommandErrorKind::InvalidInput);
    }
    assert_eq!(binding.calls.load(Ordering::SeqCst), calls);
    *binding.failure.lock().unwrap() = None;
    call(
        &service,
        "agentd/session/archive",
        json!({"sessionName":"test"}),
    )
    .await;
    call(
        &service,
        "agentd/session/resume",
        json!({"sessionName":"test"}),
    )
    .await;
    assert_eq!(
        handler_commands.prompt(blocks).await.unwrap_err().kind,
        CommandErrorKind::Offline
    );
    assert_eq!(binding.calls.load(Ordering::SeqCst), calls);
    assert!(
        call(&service, "agentd/session/prompt", params)
            .await
            .get("result")
            .is_some()
    );
    service.shutdown().await;
}

#[tokio::test]
async fn http_history_projects_non_codex_facts_without_private_evidence() {
    let root = tempfile::tempdir().unwrap();
    let adapter = Arc::new(Adapter {
        bindings: Mutex::new(vec![]),
    });
    let service = Arc::new(
        Service::new(
            root.path().into(),
            BTreeMap::from([("fixture".into(), adapter.clone())]),
            vec![],
            Duration::from_secs(1),
        )
        .unwrap(),
    );
    call(
        &service,
        "agentd/session/new",
        json!({"sessionName":"test","spec":{"runtimeName":"fixture","cwd":"/adapter/owned"}}),
    )
    .await;
    let binding = adapter.bindings.lock().unwrap()[0].clone();
    let payload = json!({"messageId":"independent-item","content":[{"type":"text","text":"independent complete fact"}]});
    for transient in [true, false] {
        binding
            .events
            .lock()
            .await
            .submit(mass_runtime::RuntimeFact {
                event_type: if transient {
                    "agent.message_chunk"
                } else {
                    "agent.message"
                }
                .into(),
                item_id: Some("independent-item".into()),
                payload: payload.clone(),
                evidence: json!({"version":9,"secretProof":"private-independent-proof"}),
                transient,
            })
            .unwrap();
    }
    let response = call(
        &service,
        "agentd/session/get_event",
        json!({"sessionName":"test"}),
    )
    .await;
    let events = response["result"]["events"].as_array().unwrap();
    assert_eq!(events.len(), 1);
    assert_eq!(events[0]["seq"], 1);
    assert_eq!(events[0]["schemaVersion"], 2);
    assert_eq!(events[0]["payload"], payload);
    assert!(!response.to_string().contains("private-independent-proof"));
    service.shutdown().await;
}
