//! Persisted session identity and configuration values; no runtime ownership.
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::path::PathBuf;

#[derive(Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct SessionSpec {
    pub runtime_name: String,
    pub cwd: PathBuf,
}

#[derive(Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct SessionMetadata {
    pub session_name: String,
    pub session_id: String,
    pub format_version: u32,
    pub instance: Value,
    pub recovery: Value,
    pub history_mode: String,
    pub spec: SessionSpec,
    pub config_options: Value,
    pub capabilities: Value,
}
