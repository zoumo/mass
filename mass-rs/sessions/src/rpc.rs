use serde_json::{Value, json};
#[derive(Debug)]
pub enum RpcError {
    InvalidParams(String),
    Internal(anyhow::Error),
    Structured(Value),
    MethodNotFound,
}
impl From<anyhow::Error> for RpcError {
    fn from(error: anyhow::Error) -> Self {
        Self::Internal(error)
    }
}
impl RpcError {
    pub fn response(self) -> Value {
        match self {
            Self::Structured(v) => v,
            Self::InvalidParams(message) => json!({"code":-32602,"message":message}),
            Self::Internal(error) => json!({"code":-32603,"message":error.to_string()}),
            Self::MethodNotFound => json!({"code":-32601,"message":"method not found"}),
        }
    }
}
pub fn unsupported() -> RpcError {
    RpcError::Structured(error(
        -32602,
        "unsupported_capability",
        "operation is not supported by this session",
        json!({}),
    ))
}
pub fn error(code: i64, kind: &str, message: &str, mut data: Value) -> Value {
    data["kind"] = json!(kind);
    json!({"code":code,"message":message,"data":data})
}

impl From<mass_runtime::CommandError> for RpcError {
    fn from(error: mass_runtime::CommandError) -> Self {
        let code = if matches!(
            error.kind,
            mass_runtime::CommandErrorKind::InvalidInput
                | mass_runtime::CommandErrorKind::Unsupported
                | mass_runtime::CommandErrorKind::Busy
                | mass_runtime::CommandErrorKind::Offline
                | mass_runtime::CommandErrorKind::Closing
                | mass_runtime::CommandErrorKind::Backpressure
                | mass_runtime::CommandErrorKind::Cancelling
        ) {
            -32602
        } else {
            -32603
        };
        Self::Structured(crate::rpc::error(
            code,
            error.kind.as_str(),
            error.message,
            serde_json::to_value(&error).expect("command error serialization"),
        ))
    }
}
