use mass_runtime::{
    Acceptance, AcceptanceEvidence, CommandError, CommandErrorKind, InputBlock, InputCommand,
    InputReceipt, RuntimeFuture, SessionCommands, SessionControl, public_id,
};
use serde_json::{Value, json};
use std::sync::{
    Arc, Mutex,
    atomic::{AtomicUsize, Ordering},
};
use tokio::sync::oneshot;

#[derive(Default)]
struct Control {
    calls: AtomicUsize,
    received: Mutex<Vec<Vec<Value>>>,
    reply: Mutex<Option<Result<InputReceipt, CommandError>>>,
    reject: Mutex<Option<CommandError>>,
    abandon: bool,
}
impl SessionControl for Control {
    fn begin(
        self: Arc<Self>,
        command: InputCommand,
    ) -> Result<oneshot::Receiver<Result<InputReceipt, CommandError>>, CommandError> {
        self.calls.fetch_add(1, Ordering::SeqCst);
        self.received.lock().unwrap().push(
            command
                .blocks
                .into_iter()
                .map(|InputBlock::Text { text }| json!({"type":"text","text":text}))
                .collect(),
        );
        if let Some(error) = self.reject.lock().unwrap().clone() {
            return Err(error);
        }
        let (tx, rx) = oneshot::channel();
        if !self.abandon {
            tx.send(
                self.reply
                    .lock()
                    .unwrap()
                    .clone()
                    .unwrap_or(Ok(InputReceipt {
                        operation_id: command.operation_id.clone(),
                        submission_id: command.operation_id,
                        message_id: "message".into(),
                        acceptance: Acceptance::NativeRecorded,
                    })),
            )
            .unwrap();
        }
        Ok(rx)
    }
    fn cancel(self: Arc<Self>) -> RuntimeFuture<'static, Result<(), CommandError>> {
        Box::pin(async move {
            self.calls.fetch_add(1, Ordering::SeqCst);
            Ok(())
        })
    }
}

#[tokio::test]
async fn invalid_or_oversized_prompts_never_reach_attachment_admission() {
    let control = Arc::new(Control::default());
    let commands = SessionCommands::new(control.clone());
    for prompt in [
        json!([]),
        json!([{"type":"text","text":""}]),
        json!([{"type":"image","data":"x"}]),
        json!([{"type":"text","text":1}]),
        json!([{"type":"text","text":"x","extra":true}]),
        json!([{"type":"text","text":"x".repeat(1024 * 1024)}]),
    ] {
        let err = commands
            .prompt(prompt.as_array().unwrap().clone())
            .await
            .unwrap_err();
        assert_eq!(err.kind, CommandErrorKind::InvalidInput);
    }
    assert_eq!(control.calls.load(Ordering::SeqCst), 0);
}

#[tokio::test]
async fn commands_and_clones_keep_the_exact_attachment_and_text_blocks() {
    let first = Arc::new(Control::default());
    let replacement = Arc::new(Control::default());
    let commands = SessionCommands::new(first.clone());
    let other = SessionCommands::new(replacement.clone());
    let blocks = vec![
        json!({"type":"text","text":"你好"}),
        json!({"type":"text","text":""}),
    ];
    assert_eq!(
        commands
            .clone()
            .prompt(blocks.clone())
            .await
            .unwrap()
            .acceptance,
        Acceptance::NativeRecorded
    );
    commands.cancel().await.unwrap();
    assert_eq!(first.calls.load(Ordering::SeqCst), 2);
    assert_eq!(*first.received.lock().unwrap(), vec![blocks]);
    assert_eq!(replacement.calls.load(Ordering::SeqCst), 0);
    other.cancel().await.unwrap();
    assert_eq!(replacement.calls.load(Ordering::SeqCst), 1);
}

#[tokio::test]
async fn admission_and_native_response_errors_preserve_structured_uncertainty() {
    let mut expected = CommandError::new(CommandErrorKind::AcceptanceUnknown, "late ACK");
    expected.evidence = Some(AcceptanceEvidence {
        sent: true,
        native_recorded: true,
        native_accepted: false,
    });
    for admission in [true, false] {
        let control = Arc::new(Control::default());
        if admission {
            *control.reject.lock().unwrap() = Some(expected.clone());
        } else {
            *control.reply.lock().unwrap() = Some(Err(expected.clone()));
        }
        let err = SessionCommands::new(control)
            .prompt(vec![json!({"type":"text","text":"x"})])
            .await
            .unwrap_err();
        assert_eq!(err.kind, expected.kind);
        assert!(err.operation_id.is_some());
        assert_eq!(
            serde_json::to_value(err.evidence).unwrap(),
            serde_json::to_value(&expected.evidence).unwrap()
        );
    }
}

#[tokio::test]
async fn a_lost_submission_observer_reports_unknown_and_never_a_success_receipt() {
    let commands = SessionCommands::new(Arc::new(Control {
        abandon: true,
        ..Default::default()
    }));
    let error = commands
        .prompt(vec![json!({"type":"text","text":"x"})])
        .await
        .unwrap_err();
    assert_eq!(error.kind, CommandErrorKind::AcceptanceUnknown);
    assert!(error.message.contains("observer disconnected"));
}

#[tokio::test]
async fn unsupported_operations_do_not_call_the_native_control() {
    let control = Arc::new(Control::default());
    let commands = SessionCommands::new(control.clone());
    assert!(commands.set_config_option("model", "x").await.is_err());
    assert_eq!(control.calls.load(Ordering::SeqCst), 0);
}

#[test]
fn public_identity_is_opaque_type_qualified_and_distinguishes_utf8_keys() {
    assert_eq!(public_id("message", "a_é").len(), "message_".len() + 64);
    assert_ne!(public_id("message", "ab"), public_id("thought", "ab"));
    assert_ne!(public_id("message", "a:b"), public_id("message", "ab"));
}
