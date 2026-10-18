use mass_domain::SessionMetadata;
use mass_journal::{
    append_record, discard_uncommitted_tail, history, load, save, validate_metadata,
};
use serde_json::{Value, json};

fn event(seq: u64, kind: &str) -> Value {
    json!({"schemaVersion":2,"sessionName":"test","sessionId":"opaque","time":"2026-10-17T00:00:00Z","seq":seq,"type":kind,"itemId":"opaque-item","payload":{"messageId":"opaque-item","content":[]}})
}
fn record(event: Value) -> Value {
    json!({"event":event,"evidence":{"version":1,"opaque":true}})
}

fn metadata(root: &std::path::Path) -> SessionMetadata {
    serde_json::from_value(json!({"sessionName":"test", "sessionId":"018f0000-0000-7000-8000-000000000001",
        "formatVersion":2, "instance":{"identity":"instance","type":"codex","version":1},
        "recovery":{"opaque":"binding"},
        "historyMode":"paginated","spec":{"runtimeName":"codex","cwd":root},"configOptions":[],"capabilities":{}})).unwrap()
}

#[test]
fn metadata_round_trip_and_binding_changes_are_checked_without_rewriting_originals() {
    let root = tempfile::tempdir().unwrap();
    let mut value = metadata(root.path());
    save(root.path(), &value).unwrap();
    validate_metadata(root.path(), &value).unwrap();
    let loaded = load(root.path()).unwrap();
    assert_eq!(
        serde_json::to_value(&loaded["test"]).unwrap(),
        serde_json::to_value(&value).unwrap()
    );
    let path = mass_journal::directory(root.path(), &value).join("metadata.json");
    let original = std::fs::read(&path).unwrap();
    value.spec.runtime_name = "other".into();
    assert!(validate_metadata(root.path(), &value).is_err());
    assert_eq!(std::fs::read(path).unwrap(), original);
}

#[test]
fn failed_atomic_metadata_replacement_leaves_no_partial_file_or_tempfile() {
    let root = tempfile::tempdir().unwrap();
    let value = metadata(root.path());
    let dir = mass_journal::directory(root.path(), &value);
    std::fs::create_dir_all(dir.join("metadata.json")).unwrap();
    assert!(save(root.path(), &value).is_err());
    assert!(dir.join("metadata.json").is_dir());
    assert_eq!(std::fs::read_dir(dir).unwrap().count(), 1);
}

#[test]
fn duplicate_names_and_unsupported_metadata_are_rejected() {
    for mutation in [
        "duplicate",
        "format",
        "instance",
        "identity",
        "history",
        "runtime",
        "config",
        "capabilities",
    ] {
        let root = tempfile::tempdir().unwrap();
        let mut value = metadata(root.path());
        save(root.path(), &value).unwrap();
        match mutation {
            "duplicate" => value.session_id = "018f0000-0000-7000-8000-000000000003".into(),
            "format" => value.format_version = 1,
            "instance" => value.instance = json!({}),
            "history" => value.history_mode = "unsupported".into(),
            "runtime" => value.spec.runtime_name.clear(),
            "config" => value.config_options = json!({}),
            "capabilities" => value.capabilities = json!([]),
            _ => value.session_id.clear(),
        }
        let path = if mutation == "duplicate" {
            let directory = mass_journal::directory(root.path(), &value);
            std::fs::create_dir_all(&directory).unwrap();
            directory.join("metadata.json")
        } else {
            mass_journal::directory(root.path(), &metadata(root.path())).join("metadata.json")
        };
        std::fs::write(path, serde_json::to_vec(&value).unwrap()).unwrap();
        assert!(load(root.path()).is_err(), "{mutation}");
    }
}

#[test]
fn pagination_checks_corruption_even_beyond_the_requested_page() {
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("events.jsonl");
    assert!(history(&path, 0, 100).unwrap().is_empty());
    for seq in 1..=3 {
        append_record(&path, &record(event(seq, "agent.message"))).unwrap();
    }
    assert_eq!(
        history(&path, 1, 1).unwrap(),
        vec![event(2, "agent.message")]
    );
    append_record(&path, &record(event(5, "agent.message"))).unwrap();
    assert!(history(&path, 0, 1).is_err());
}

#[test]
fn only_newline_terminated_durable_records_are_committed_and_tail_repair_is_explicit() {
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("events.jsonl");
    let committed = format!("{}\n", record(event(1, "user.message"))).into_bytes();
    for tail in [
        b"{\"seq\":2,\"type\":\"agent.message\"}".as_slice(),
        b"{broken",
    ] {
        let bytes = [committed.as_slice(), tail].concat();
        std::fs::write(&path, &bytes).unwrap();
        assert_eq!(history(&path, 0, 10).unwrap().len(), 1);
        assert_eq!(std::fs::read(&path).unwrap(), bytes);
        discard_uncommitted_tail(&path).unwrap();
        assert_eq!(std::fs::read(&path).unwrap(), committed);
    }
    for invalid in [
        json!({"seq":2,"type":"agent.message"}),
        json!({"seq":1,"type":"session.state_update"}),
        Value::Null,
    ] {
        let invalid = record(invalid);
        std::fs::write(&path, format!("{invalid}\n")).unwrap();
        assert!(history(&path, 0, 10).is_err());
    }
}

#[test]
fn old_formats_and_private_fields_in_public_pages_fail_without_rewriting() {
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("events.jsonl");
    for corruption in ["old", "public-private-field"] {
        std::fs::write(&path, format!("{}\n", record(event(1, "agent.message")))).unwrap();
        let mut last = record(event(2, "agent.message"));
        match corruption {
            "old" => {
                last = json!({"schemaVersion":1,"seq":2,"type":"agent.message"});
            }
            _ => {
                last["event"]["payload"]["source"] = json!({"private":"must not escape"});
            }
        }
        append_record(&path, &last).unwrap();
        let original = std::fs::read(&path).unwrap();
        assert!(history(&path, 0, 1).is_err(), "{corruption}");
        assert_eq!(std::fs::read(&path).unwrap(), original);
    }
}

#[test]
fn private_versions_belong_to_the_adapter_and_public_envelopes_are_strict() {
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("events.jsonl");
    let mut row = record(event(1, "agent.message"));
    row["evidence"] = json!({"adapterSpecificRevision":"opaque-v9"});
    append_record(&path, &row).unwrap();
    assert_eq!(history(&path, 0, 1).unwrap().len(), 1);
    row["unexpected"] = json!(true);
    std::fs::write(&path, format!("{row}\n")).unwrap();
    assert!(history(&path, 0, 1).is_err());
}
