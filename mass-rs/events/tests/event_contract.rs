use mass_events::Events;
use mass_runtime::RuntimeEvents;
use serde_json::{Value, json};

fn payload() -> Value {
    json!({"messageId":"opaque-item", "content":[{"type":"text","text":"complete"}]})
}
fn events(path: &std::path::Path) -> Events {
    Events::new("test".into(), "mass".into(), path.into())
}
fn fact(preview: bool) -> mass_runtime::RuntimeFact {
    mass_runtime::RuntimeFact {
        event_type: if preview {
            "agent.message_chunk"
        } else {
            "agent.message"
        }
        .into(),
        item_id: Some("opaque-item".into()),
        payload: payload(),
        evidence: json!({"version":7,"adapter":"non-codex","proof":"complete"}),
        transient: preview,
    }
}
fn validate(_: &str, event: &Value, evidence: &Value) -> anyhow::Result<()> {
    anyhow::ensure!(
        evidence["version"] == 7
            && evidence["proof"] == "complete"
            && event["payload"] == payload(),
        "invalid independent adapter proof"
    );
    Ok(())
}
fn recover(path: &std::path::Path) -> anyhow::Result<Events> {
    Events::resume(
        "test".into(),
        "mass".into(),
        path.into(),
        || Ok(()),
        validate,
    )
}

#[tokio::test]
async fn full_fact_is_flushed_before_live_publication_and_previews_consume_no_sequence() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("events.jsonl");
    let mut core = events(&path);
    let mut live = core.subscribe();
    let mut peer = core.subscribe();
    core.submit(fact(true)).unwrap();
    let preview = live.try_recv().unwrap();
    assert!(preview.value().get("seq").is_none());
    assert!(!path.exists());
    core.submit(fact(false)).unwrap();
    let full = live.try_recv().unwrap();
    assert_eq!(full.value()["seq"], 1);
    peer.try_recv().unwrap();
    assert!(full.same_object(&peer.try_recv().unwrap()));
    let record = mass_journal::records(&path, 0, 10).unwrap().remove(0);
    assert_eq!(record["event"], *full.value());
    assert_eq!(record["evidence"], fact(false).evidence);
    assert!(full.value().get("evidence").is_none());
    assert_eq!(
        mass_journal::history(&path, 0, 10).unwrap(),
        vec![full.value().clone()]
    );
    assert!(
        core.subscribe().try_recv().is_err(),
        "new subscriptions are live-only"
    );
}

#[tokio::test]
async fn append_failure_publishes_only_live_error_and_permanently_seals_writer() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("events.jsonl");
    std::fs::create_dir(&path).unwrap();
    let mut core = events(&path);
    let mut live = core.subscribe();
    assert!(core.submit(fact(false)).is_err());
    let failure = live.try_recv().unwrap();
    assert_eq!(failure.value()["type"], "session.error");
    assert!(failure.value().get("seq").is_none());
    std::fs::remove_dir(&path).unwrap();
    assert!(core.submit(fact(false)).is_err());
    assert!(
        !path.exists(),
        "repairing path cannot revive the failed writer"
    );
    assert!(live.try_recv().is_err());
}

#[tokio::test]
async fn strict_recovery_validates_every_source_before_repair_and_continues_sequence() {
    use std::io::Write;
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("events.jsonl");
    let mut core = events(&path);
    core.submit(fact(false)).unwrap();
    let original = std::fs::read(&path).unwrap();
    std::fs::OpenOptions::new()
        .append(true)
        .open(&path)
        .unwrap()
        .write_all(b"{tail")
        .unwrap();
    let mut recovered = recover(&path).unwrap();
    assert_eq!(std::fs::read(&path).unwrap(), original);
    recovered.submit(fact(false)).unwrap();
    assert_eq!(mass_journal::history(&path, 0, 10).unwrap()[1]["seq"], 2);
    let mut corrupt: Value = serde_json::from_slice(&original).unwrap();
    corrupt["evidence"]["proof"] = json!("another-thread");
    let corrupt_bytes = format!("{corrupt}\n{{tail").into_bytes();
    std::fs::write(&path, &corrupt_bytes).unwrap();
    assert!(recover(&path).is_err());
    assert_eq!(std::fs::read(&path).unwrap(), corrupt_bytes);
}

#[tokio::test]
async fn provider_payload_rejection_does_not_repair_the_uncommitted_tail() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("events.jsonl");
    events(&path).submit(fact(false)).unwrap();
    let mut bytes = std::fs::read(&path).unwrap();
    bytes.extend_from_slice(b"tail");
    std::fs::write(&path, &bytes).unwrap();
    assert!(
        Events::resume(
            "test".into(),
            "mass".into(),
            path.clone(),
            || Ok(()),
            |_, _, _| anyhow::bail!("invalid official payload")
        )
        .is_err()
    );
    assert_eq!(std::fs::read(path).unwrap(), bytes);
}

#[tokio::test]
async fn disconnect_clears_state_without_synthesizing_history_and_slow_readers_lag() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("events.jsonl");
    let mut core = events(&path);
    let mut slow = core.subscribe();
    for _ in 0..257 {
        core.submit(mass_runtime::RuntimeFact {
            event_type: "session.state_update".into(),
            item_id: None,
            payload: json!({"state":"working"}),
            evidence: Value::Null,
            transient: true,
        })
        .unwrap();
    }
    assert!(matches!(
        slow.try_recv(),
        Err(tokio::sync::broadcast::error::TryRecvError::Lagged(_))
    ));
    core.disconnect("native EOF");
    assert!(core.snapshot()["state"].is_null());
    assert!(!path.exists());
}

#[tokio::test]
async fn public_validation_precedes_all_adapter_checks_and_binding_failure_preserves_tail() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("events.jsonl");
    let mut core = events(&path);
    core.submit(fact(false)).unwrap();
    core.submit(fact(false)).unwrap();
    let original = std::fs::read_to_string(&path).unwrap();
    let mut rows: Vec<Value> = original
        .lines()
        .map(|s| serde_json::from_str(s).unwrap())
        .collect();
    rows[1]["event"]["sessionId"] = json!("other");
    let damaged = format!(
        "{}\n{{\"unterminated\":true}}",
        rows.iter()
            .map(Value::to_string)
            .collect::<Vec<_>>()
            .join("\n")
    );
    std::fs::write(&path, &damaged).unwrap();
    assert!(
        Events::resume(
            "test".into(),
            "mass".into(),
            path.clone(),
            || panic!("binding validation must follow all public identities"),
            |_, _, _| panic!("private validation must follow all public identities")
        )
        .is_err()
    );
    assert_eq!(std::fs::read_to_string(&path).unwrap(), damaged);
    let valid_tail = format!("{original}{{\"uncommitted\":true}}");
    std::fs::write(&path, &valid_tail).unwrap();
    assert!(
        Events::resume(
            "test".into(),
            "mass".into(),
            path.clone(),
            || anyhow::bail!("unsupported ownership binding"),
            |_, _, _| panic!("private facts must follow binding validation")
        )
        .is_err()
    );
    assert_eq!(std::fs::read_to_string(&path).unwrap(), valid_tail);
}
