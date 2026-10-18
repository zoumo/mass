use mass_domain::{SessionMetadata, SessionSpec};
use serde_json::json;

#[test]
fn persisted_identity_and_configuration_use_the_native_camel_case_contract() {
    let wire = json!({
        "sessionName":"研究", "sessionId":"native-id",
        "formatVersion":2, "instance":{"identity":"instance","type":"codex","version":1}, "recovery":{"opaque":"binding"},
        "historyMode":"paginated", "spec":{"runtimeName":"local", "cwd":"/tmp/work"},
        "configOptions":[], "capabilities":{"prompt":false}
    });
    let metadata: SessionMetadata = serde_json::from_value(wire.clone()).unwrap();
    assert_eq!(metadata.session_name, "研究");
    assert_eq!(metadata.session_id, "native-id");
    assert_eq!(metadata.spec.runtime_name, "local");
    assert_eq!(serde_json::to_value(metadata).unwrap(), wire);
}

#[test]
fn missing_identity_or_runtime_spec_is_not_silently_defaulted() {
    assert!(serde_json::from_value::<SessionMetadata>(json!({"sessionName":"x"})).is_err());
    assert!(serde_json::from_value::<SessionSpec>(json!({"cwd":"/tmp"})).is_err());
    assert!(serde_json::from_value::<SessionSpec>(json!({"runtimeName":"x"})).is_err());
}
