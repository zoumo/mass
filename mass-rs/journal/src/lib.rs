//! File Journal: atomic metadata, flushed appends and strict sequential reads.
use mass_domain::SessionMetadata;
use serde_json::Value;
use std::{collections::BTreeMap, io::Write, path::Path};

pub fn load(data_dir: &Path) -> anyhow::Result<BTreeMap<String, SessionMetadata>> {
    let root = data_dir.join("sessions");
    std::fs::create_dir_all(&root)?;
    let mut sessions = BTreeMap::new();
    for entry in std::fs::read_dir(root)? {
        let entry_path = entry?.path();
        let path = entry_path.join("metadata.json");
        if !path.exists() {
            continue;
        }
        let value: Value = serde_json::from_slice(&std::fs::read(&path)?)?;
        anyhow::ensure!(value["formatVersion"] == 2, "unsupported session format");
        let metadata: SessionMetadata = serde_json::from_value(value)?;
        validate_shape(&metadata)?;
        anyhow::ensure!(
            directory(data_dir, &metadata) == entry_path,
            "session storage identity mismatch"
        );
        anyhow::ensure!(
            !sessions.contains_key(&metadata.session_name),
            "duplicate session name in metadata"
        );
        sessions.insert(metadata.session_name.clone(), metadata);
    }
    Ok(sessions)
}

pub fn save(data_dir: &Path, metadata: &SessionMetadata) -> anyhow::Result<()> {
    validate_shape(metadata)?;
    let directory = directory(data_dir, metadata);
    std::fs::create_dir_all(&directory)?;
    let mut file = tempfile::NamedTempFile::new_in(&directory)?;
    file.write_all(&serde_json::to_vec_pretty(metadata)?)?;
    file.as_file().sync_all()?;
    file.persist_noclobber(directory.join("metadata.json"))?;
    std::fs::File::open(&directory)?.sync_all()?;
    Ok(())
}

/// An attachment must use exactly the persisted binding loaded at startup.
pub fn validate_metadata(data_dir: &Path, metadata: &SessionMetadata) -> anyhow::Result<()> {
    let path = directory(data_dir, metadata).join("metadata.json");
    let actual: Value = serde_json::from_slice(&std::fs::read(path)?)?;
    anyhow::ensure!(
        actual == serde_json::to_value(metadata)?,
        "session metadata changed or unsupported"
    );
    validate_shape(metadata)
}
fn validate_shape(metadata: &SessionMetadata) -> anyhow::Result<()> {
    anyhow::ensure!(metadata.format_version == 2, "unsupported session format");
    only_fields(&metadata.instance, &["identity", "type", "version"])?;
    anyhow::ensure!(
        !metadata.session_id.is_empty() && !metadata.session_name.trim().is_empty(),
        "missing session identity"
    );
    anyhow::ensure!(
        metadata.instance["identity"]
            .as_str()
            .is_some_and(|id| !id.is_empty())
            && metadata.instance["type"]
                .as_str()
                .is_some_and(|kind| !kind.is_empty())
            && metadata.instance["version"].as_u64().is_some_and(|v| v > 0)
            && metadata.recovery.is_object(),
        "invalid instance/recovery binding"
    );
    anyhow::ensure!(
        metadata.history_mode == "paginated"
            && !metadata.spec.runtime_name.is_empty()
            && metadata.config_options.is_array()
            && metadata.capabilities.is_object(),
        "invalid session metadata shape"
    );
    Ok(())
}
/// A fixed-size private locator encodes the pair; it is never a logical Session ID.
pub fn directory(data_dir: &Path, metadata: &SessionMetadata) -> std::path::PathBuf {
    session_directory(data_dir, &metadata.instance, &metadata.session_id)
}
pub fn session_directory(
    data_dir: &Path,
    instance: &Value,
    session_id: &str,
) -> std::path::PathBuf {
    use sha2::{Digest, Sha256};
    let key = serde_json::to_vec(&(instance["identity"].as_str(), session_id))
        .expect("identity serializes");
    data_dir
        .join("sessions")
        .join(format!("{:x}", Sha256::digest(key)))
}

/// Validate only the MASS public shape; private evidence remains adapter-owned.
pub fn validate_public_event(event: &Value) -> anyhow::Result<()> {
    only_fields(
        event,
        &[
            "schemaVersion",
            "sessionName",
            "sessionId",
            "type",
            "time",
            "payload",
            "itemId",
            "seq",
        ],
    )?;
    anyhow::ensure!(
        event["schemaVersion"] == 2,
        "unsupported public event schema"
    );
    for field in ["sessionName", "sessionId", "time"] {
        anyhow::ensure!(
            event[field].as_str().is_some_and(|s| !s.is_empty()),
            "missing public event identity/time"
        );
    }
    let kind = event["type"]
        .as_str()
        .ok_or_else(|| anyhow::anyhow!("missing event type"))?;
    let id = event["itemId"]
        .as_str()
        .filter(|s| !s.is_empty())
        .ok_or_else(|| anyhow::anyhow!("missing public item identity"))?;
    validate_payload(kind, id, &event["payload"])
}
fn only_fields(value: &Value, fields: &[&str]) -> anyhow::Result<()> {
    anyhow::ensure!(
        value
            .as_object()
            .is_some_and(|object| object.keys().all(|key| fields.contains(&key.as_str()))),
        "unsupported public fields"
    );
    Ok(())
}
fn validate_payload(kind: &str, id: &str, payload: &Value) -> anyhow::Result<()> {
    match kind {
        "user.message" | "agent.message" | "agent.thinking" => {
            only_fields(payload, &["messageId", "content", "phase"])?;
            anyhow::ensure!(
                payload["messageId"] == id && payload["content"].is_array(),
                "invalid public message"
            );
        }
        "agent.tool_call" | "agent.tool_result" => {
            only_fields(
                payload,
                &[
                    "id",
                    "name",
                    "kind",
                    "title",
                    "phase",
                    "inputRepresentation",
                    "rawInput",
                    "status",
                    "exitCode",
                    "durationMs",
                    "rawOutput",
                    "content",
                    "output",
                    "callContext",
                ],
            )?;
            anyhow::ensure!(payload["id"] == id, "invalid public tool identity");
            only_fields(&payload["rawInput"], &["command", "cwd"])?;
            if let Some(output) = payload.get("output") {
                only_fields(output, &["availability", "completeness", "truncation"])?;
                if let Some(truncation) = output.get("truncation") {
                    only_fields(truncation, &["omittedBytes", "retainedBytes"])?;
                }
            }
            if let Some(call) = payload.get("callContext").filter(|v| **v != "missing") {
                validate_payload("agent.tool_call", id, call)?;
            }
        }
        _ => anyhow::bail!("unsupported public history type"),
    }
    for field in [
        "phase",
        "name",
        "kind",
        "title",
        "inputRepresentation",
        "status",
        "rawOutput",
    ] {
        anyhow::ensure!(
            payload.get(field).is_none_or(|value| value.is_string()),
            "invalid public text field"
        );
    }
    if let Some(input) = payload.get("rawInput") {
        anyhow::ensure!(
            input["command"].is_string() && input["cwd"].is_string(),
            "invalid public execution input"
        );
    }
    if let Some(content) = payload.get("content") {
        for block in content
            .as_array()
            .ok_or_else(|| anyhow::anyhow!("invalid public content"))?
        {
            only_fields(block, &["type", "text", "channel", "index"])?;
            anyhow::ensure!(
                block["type"] == "text" && block["text"].is_string(),
                "invalid public text block"
            );
        }
    }
    Ok(())
}

pub fn append_record(path: &Path, record: &Value) -> anyhow::Result<()> {
    let mut file = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)?;
    let mut line = serde_json::to_vec(record)?;
    line.push(b'\n');
    file.write_all(&line)?;
    file.flush()?;
    Ok(())
}

/// Public history is exactly the committed public projection, never private evidence.
pub fn history(path: &Path, after_seq: u64, limit: usize) -> anyhow::Result<Vec<Value>> {
    Ok(records(path, after_seq, limit)?
        .into_iter()
        .map(|record| record["event"].clone())
        .collect())
}

/// Private records are only for adapter-owned recovery validation.
pub fn records(path: &Path, after_seq: u64, limit: usize) -> anyhow::Result<Vec<Value>> {
    let bytes = match std::fs::read(path) {
        Ok(bytes) => bytes,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => return Err(error.into()),
    };
    let mut result = Vec::new();
    let mut previous = 0;
    let mut lines = bytes.split_inclusive(|b| *b == b'\n').peekable();
    while let Some(line) = lines.next() {
        // A partial final append is not a committed record, even if its JSON parses.
        if !line.ends_with(b"\n") && lines.peek().is_none() {
            break;
        }
        let record: Value = serde_json::from_slice(line)?;
        only_fields(&record, &["event", "evidence"])?;
        let event = &record["event"];
        anyhow::ensure!(
            event["schemaVersion"] == 2 && record["evidence"].is_object(),
            "unsupported history record format"
        );
        validate_public_event(event)?;
        let seq = event["seq"]
            .as_u64()
            .ok_or_else(|| anyhow::anyhow!("history record missing seq"))?;
        anyhow::ensure!(seq == previous + 1, "history sequence corruption");
        anyhow::ensure!(
            matches!(
                event["type"].as_str(),
                Some(
                    "user.message"
                        | "agent.message"
                        | "agent.thinking"
                        | "agent.tool_call"
                        | "agent.tool_result"
                )
            ),
            "non-durable history record"
        );
        previous = seq;
        if seq > after_seq && result.len() < limit {
            result.push(record);
        }
    }
    Ok(result)
}

/// Caller must validate history under the lifecycle lock before discarding the final tail.
pub fn discard_uncommitted_tail(path: &Path) -> anyhow::Result<()> {
    match std::fs::read(path) {
        Ok(bytes) => {
            let committed = bytes
                .iter()
                .rposition(|byte| *byte == b'\n')
                .map_or(0, |index| index + 1);
            if committed < bytes.len() {
                std::fs::OpenOptions::new()
                    .write(true)
                    .open(path)?
                    .set_len(committed as u64)?;
            }
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(error) => return Err(error.into()),
    }
    Ok(())
}

/// Validate the complete public history against its owning metadata, including
/// records outside a requested page. Querying never repairs an uncommitted tail.
pub fn history_for(
    path: &Path,
    name: &str,
    id: &str,
    after_seq: u64,
    limit: usize,
) -> anyhow::Result<Vec<Value>> {
    let rows = records(path, 0, usize::MAX)?;
    for row in &rows {
        anyhow::ensure!(
            row["event"]["sessionName"] == name && row["event"]["sessionId"] == id,
            "history session identity mismatch"
        );
    }
    Ok(rows
        .into_iter()
        .filter(|row| row["event"]["seq"].as_u64().unwrap() > after_seq)
        .take(limit)
        .map(|row| row["event"].clone())
        .collect())
}
