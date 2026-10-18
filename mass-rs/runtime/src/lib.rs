//! Attachment-bound commands and normalized MASS facts. No provider, Journal or HTTP dependencies.
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::{future::Future, pin::Pin, sync::Arc};
use tokio::sync::oneshot;

pub type RuntimeFuture<'a, T> = Pin<Box<dyn Future<Output = T> + Send + 'a>>;

/// Commands and results carry MASS data only. The adapter owns native translation.
pub trait SessionControl: Send + Sync {
    fn begin(
        self: Arc<Self>,
        command: InputCommand,
    ) -> Result<oneshot::Receiver<Result<InputReceipt, CommandError>>, CommandError>;
    fn cancel(self: Arc<Self>) -> RuntimeFuture<'static, Result<(), CommandError>>;
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case", deny_unknown_fields)]
pub enum InputBlock {
    Text { text: String },
}
#[derive(Clone, Debug)]
pub struct InputCommand {
    pub operation_id: String,
    pub blocks: Vec<InputBlock>,
}
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Acceptance {
    NativeRecorded,
}
#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct InputReceipt {
    pub operation_id: String,
    pub submission_id: String,
    pub message_id: String,
    pub acceptance: Acceptance,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum CommandErrorKind {
    InvalidInput,
    #[serde(rename = "unsupported_capability")]
    Unsupported,
    Busy,
    Offline,
    Closing,
    Backpressure,
    Cancelling,
    Rejected,
    Cancelled,
    AcceptanceUnknown,
    CancelUnknown,
}
impl CommandErrorKind {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::InvalidInput => "invalid_input",
            Self::Unsupported => "unsupported_capability",
            Self::Busy => "busy",
            Self::Offline => "offline",
            Self::Closing => "closing",
            Self::Backpressure => "backpressure",
            Self::Cancelling => "cancelling",
            Self::Rejected => "rejected",
            Self::Cancelled => "cancelled",
            Self::AcceptanceUnknown => "acceptance_unknown",
            Self::CancelUnknown => "cancel_unknown",
        }
    }
}
#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AcceptanceEvidence {
    pub sent: bool,
    pub native_accepted: bool,
    pub native_recorded: bool,
}
#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CommandError {
    pub kind: CommandErrorKind,
    pub message: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub operation_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub submission_id: Option<String>,
    #[serde(flatten, skip_serializing_if = "Option::is_none")]
    pub evidence: Option<AcceptanceEvidence>,
}
impl CommandError {
    pub fn new(kind: CommandErrorKind, message: &'static str) -> Self {
        Self {
            kind,
            message,
            operation_id: None,
            submission_id: None,
            evidence: None,
        }
    }
}
impl std::fmt::Display for CommandError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.message)
    }
}
impl std::error::Error for CommandError {}

/// A normalized public fact and opaque, adapter-versioned validation evidence.
/// Evidence is persisted with a complete fact and is never delivered to consumers.
pub struct RuntimeFact {
    pub event_type: String,
    pub item_id: Option<String>,
    pub payload: Value,
    pub evidence: Value,
    pub transient: bool,
}

/// Calls are serialized by the attachment. Complete facts may block on Journal I/O;
/// transient observations have no durability promise. Disconnect never adds a full fact.
pub trait RuntimeEvents: Send {
    fn submit(&mut self, fact: RuntimeFact) -> anyhow::Result<()>;
    fn detach(&mut self);
    fn disconnect(&mut self, reason: &str);
}

/// Opaque, type-qualified identity; consumers must never decode it.
pub fn public_id(kind: &str, key: &str) -> String {
    use sha2::{Digest, Sha256};
    format!("{kind}_{:x}", Sha256::digest(key.as_bytes()))
}

/// Commands retain the exact attachment; handlers never resolve a replacement by name.
#[derive(Clone)]
pub struct SessionCommands {
    handle: Arc<dyn SessionControl>,
}
impl SessionCommands {
    pub fn new(handle: Arc<dyn SessionControl>) -> Self {
        Self { handle }
    }
    pub async fn prompt(&self, content: Vec<Value>) -> Result<InputReceipt, CommandError> {
        let operation_id = uuid::Uuid::now_v7().to_string();
        self.input(content, operation_id.clone())
            .await
            .map_err(|mut error| {
                error.operation_id = Some(operation_id);
                error
            })
    }
    async fn input(
        &self,
        content: Vec<Value>,
        operation_id: String,
    ) -> Result<InputReceipt, CommandError> {
        let invalid = || {
            CommandError::new(
                CommandErrorKind::InvalidInput,
                "input must contain nonempty MASS text blocks within 1 MiB",
            )
        };
        if content.is_empty()
            || serde_json::to_vec(&content).map_err(|_| invalid())?.len() > 1024 * 1024
        {
            return Err(invalid());
        }
        let blocks: Vec<InputBlock> = content
            .into_iter()
            .map(serde_json::from_value)
            .collect::<Result<_, _>>()
            .map_err(|_| invalid())?;
        if !blocks
            .iter()
            .any(|block| matches!(block, InputBlock::Text { text } if !text.is_empty()))
        {
            return Err(invalid());
        }
        let receiver = self.handle.clone().begin(InputCommand {
            operation_id,
            blocks,
        })?;
        receiver.await.map_err(|_| {
            CommandError::new(
                CommandErrorKind::AcceptanceUnknown,
                "input observer disconnected; outcome is unknown",
            )
        })?
    }
    pub async fn cancel(&self) -> Result<(), CommandError> {
        let operation_id = uuid::Uuid::now_v7().to_string();
        self.handle.clone().cancel().await.map_err(|mut error| {
            error.operation_id = Some(operation_id);
            error
        })
    }
    pub async fn set_config_option(
        &self,
        _config_id: &str,
        _value: &str,
    ) -> Result<(), CommandError> {
        Err(CommandError::new(
            CommandErrorKind::Unsupported,
            "configuration changes are not supported",
        ))
    }
}

/// Session management owns names and persistence; the adapter owns concrete resources.
/// The factory is invoked with the Agent-returned identity before routing facts.
pub type EventFactory =
    Arc<dyn Fn(&str) -> Arc<tokio::sync::Mutex<dyn RuntimeEvents>> + Send + Sync>;
pub struct OpenRequest {
    pub cwd: std::path::PathBuf,
    pub recovery: Option<(String, Value)>,
    pub connection_id: String,
    pub deadline: tokio::time::Instant,
    pub events: EventFactory,
    pub owner: Arc<dyn Send + Sync>,
}
pub struct OpenedAttachment {
    pub session_id: String,
    pub recovery: Value,
    pub control: Arc<dyn AttachmentControl>,
}
pub trait AttachmentControl: SessionControl {
    fn online(&self) -> bool;
    fn closing(&self) -> bool;
    fn close(self: Arc<Self>) -> RuntimeFuture<'static, anyhow::Result<()>>;
    fn commit(&self);
}
pub trait Runtime: Send + Sync {
    fn runtime_type(&self) -> &str;
    fn supports_recovery(&self) -> bool {
        true
    }
    fn fingerprint(&self) -> anyhow::Result<String>;
    fn validate_cwd(&self, cwd: &std::path::Path) -> anyhow::Result<()>;
    fn validate_recovery(
        &self,
        session_id: &str,
        binding: &Value,
        cwd: &std::path::Path,
    ) -> anyhow::Result<()>;
    fn status(&self) -> RuntimeFuture<'_, Value>;
    fn open(
        self: Arc<Self>,
        request: OpenRequest,
    ) -> RuntimeFuture<'static, anyhow::Result<OpenedAttachment>>;
    fn validate_history_record(&self) -> fn(&str, &Value, &Value) -> anyhow::Result<()>;
    fn close_admission(&self) -> RuntimeFuture<'_, ()>;
    fn stop(&self) -> RuntimeFuture<'_, anyhow::Result<()>>;
}

/// No caller may retry an open whose Agent result is still being settled.
#[derive(Debug)]
pub struct OpenUnknown;
impl std::fmt::Display for OpenUnknown {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("session open outcome is unknown; original owner is settling it")
    }
}
impl std::error::Error for OpenUnknown {}

/// Stable lifecycle failure with no native response or configuration contents.
#[derive(Debug)]
pub struct LifecycleError {
    pub kind: &'static str,
    pub message: &'static str,
}
impl std::fmt::Display for LifecycleError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.message)
    }
}
impl std::error::Error for LifecycleError {}
