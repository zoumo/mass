use mass_codex::{Config, Observation, ProviderRuntime, validate_history_payload};
use serde_json::{Value, json};

fn config() -> Value {
    json!({"provider":"codex","executable":"/does-not-exist","sha256":"wrong"})
}

#[tokio::test]
async fn configuration_bounds_are_rejected_before_native_spawn() {
    for (field, value, message) in [
        ("provider", json!("claude"), "unsupported provider"),
        ("sdkEventBytes", json!(1023), "sdkEventBytes"),
        ("sdkEventBytes", json!(33554433), "sdkEventBytes"),
        ("factLaneCapacity", json!(0), "factLaneCapacity"),
        ("factLaneCapacity", json!(65), "factLaneCapacity"),
        ("closeTimeoutMs", json!(0), "closeTimeoutMs"),
        ("closeTimeoutMs", json!(300001), "closeTimeoutMs"),
        ("executable", json!("relative"), "must be absolute"),
    ] {
        let mut config = config();
        config[field] = value;
        let runtime = ProviderRuntime::new(
            serde_json::from_value(config).unwrap(),
            Observation::default(),
        );
        let error = runtime
            .start()
            .await
            .err()
            .expect("invalid config must fail");
        assert!(error.to_string().contains(message), "{field}: {error}");
        runtime.stop().await;
    }
}

#[tokio::test]
async fn stopped_runtime_cannot_be_reactivated_and_stop_is_idempotent() {
    let runtime = ProviderRuntime::new(
        serde_json::from_value(config()).unwrap(),
        Observation::default(),
    );
    runtime.close_admission().await;
    assert!(
        runtime
            .start()
            .await
            .err()
            .unwrap()
            .to_string()
            .contains("stopped")
    );
    runtime.stop().await;
    runtime.stop().await;
}

#[tokio::test]
async fn reviewed_binary_hash_and_version_are_both_required() {
    use sha2::{Digest, Sha256};
    let binary = "/usr/bin/true";
    let mut config = config();
    config["executable"] = json!(binary);
    let runtime = ProviderRuntime::new(
        serde_json::from_value(config.clone()).unwrap(),
        Observation::default(),
    );
    assert!(
        runtime
            .start()
            .await
            .err()
            .unwrap()
            .to_string()
            .contains("SHA256")
    );
    config["sha256"] = json!(format!(
        "{:x}",
        Sha256::digest(std::fs::read(binary).unwrap())
    ));
    let runtime = ProviderRuntime::new(
        serde_json::from_value(config).unwrap(),
        Observation::default(),
    );
    assert!(
        runtime
            .start()
            .await
            .err()
            .unwrap()
            .to_string()
            .contains("unsupported native version")
    );
    runtime.stop().await;
}

#[test]
fn native_configuration_rejects_unknown_options_and_retains_finite_defaults() {
    let parsed: Config = serde_json::from_value(config()).unwrap();
    assert_eq!(parsed.fact_lane_capacity, 64);
    assert_eq!(parsed.sdk_event_bytes, 32 * 1024 * 1024);
    assert_eq!(parsed.close_timeout_ms, 30_000);
    let mut invalid = config();
    invalid["invented"] = json!(true);
    assert!(serde_json::from_value::<Config>(invalid).is_err());
}

fn agent_payload() -> Value {
    json!({"source":{"itemId":"a","completionEvidence":"item/completed"},
        "nativeItem":{"type":"agentMessage","id":"a","text":"native full","phase":"final_answer","memoryCitation":null},
        "content":[{"type":"text","text":"native full"}],"phase":"final_answer"})
}

#[test]
fn saved_agent_full_must_match_the_official_item_text_identity_phase_and_evidence() {
    let valid = agent_payload();
    validate_history_payload("agent.message", &valid).unwrap();
    for (pointer, replacement) in [
        ("/content/0/text", json!("synthesized")),
        ("/phase", json!("commentary")),
        ("/source/itemId", json!("other")),
        ("/source/completionEvidence", json!("EOF")),
        ("/nativeItem/text", Value::Null),
    ] {
        let mut invalid = valid.clone();
        *invalid.pointer_mut(pointer).unwrap() = replacement;
        assert!(
            validate_history_payload("agent.message", &invalid).is_err(),
            "{pointer}"
        );
    }
    assert!(validate_history_payload("agent.message_chunk", &valid).is_err());
}

#[test]
fn file_and_in_process_composition_use_the_same_registration_fingerprint() {
    use mass_runtime::Runtime;
    let config: mass_codex::RegistrationConfig = serde_json::from_value(json!({
        "command":"/missing-reviewed-codex", "sha256":"a".repeat(64),
        "env":{"MASS_PROBE":"value"}, "secretRefs":{"MASS_SECRET":"env:MASS_SECRET_SOURCE"}
    }))
    .unwrap();
    let expected = config.fingerprint().unwrap();
    let mut upper = config.clone();
    upper.sha256.make_ascii_uppercase();
    assert_eq!(upper.fingerprint().unwrap(), expected);
    let runtime = upper.runtime(Observation::default());
    assert_eq!(Runtime::fingerprint(&runtime).unwrap(), expected);
}

#[test]
fn private_evidence_validates_the_exact_coordinate_free_public_projection() {
    let mut payload = agent_payload();
    payload["source"] = json!({"provider":"codex","nativeSessionId":"opaque-agent-session","threadId":"opaque-agent-session","turnId":"private-turn","itemId":"a","completionEvidence":"item/completed","connectionId":"018f0000-0000-7000-8000-000000000001","hostGeneration":"018f0000-0000-7000-8000-000000000002"});
    let id = mass_runtime::public_id(
        "message",
        &json!(["codex", "opaque-agent-session", "a"]).to_string(),
    );
    let event = json!({"schemaVersion":2,"type":"agent.message","itemId":id,"payload":{"messageId":id,"content":payload["content"],"phase":"final_answer"}});
    let evidence = json!({"adapter":"codex","version":1,"payload":payload});
    mass_codex::validate_history_record("opaque-agent-session", &event, &evidence).unwrap();
    for (pointer, value) in [
        ("/payload/content/0/text", json!("forged")),
        ("/itemId", json!("message_00")),
        ("/payload/phase", json!("commentary")),
    ] {
        let mut corrupt = event.clone();
        *corrupt.pointer_mut(pointer).unwrap() = value;
        assert!(
            mass_codex::validate_history_record("opaque-agent-session", &corrupt, &evidence)
                .is_err(),
            "{pointer}"
        );
    }
    let mut leaked = event.clone();
    leaked["payload"]["source"] = evidence["payload"]["source"].clone();
    assert!(
        mass_codex::validate_history_record("opaque-agent-session", &leaked, &evidence).is_err()
    );
    assert!(mass_codex::validate_history_record("other-session", &event, &evidence).is_err());
    let mut unsupported = evidence;
    unsupported["version"] = json!(2);
    assert!(
        mass_codex::validate_history_record("opaque-agent-session", &event, &unsupported).is_err()
    );
}

#[test]
fn remote_schema_rejects_local_fields_unsafe_auth_and_embedded_credentials() {
    use mass_codex::RemoteRegistrationConfig as Remote;
    use mass_runtime::Runtime;
    let valid = json!({"url":"ws://127.0.0.1:1234"});
    for url in [
        "ws://localhost:1234",
        "ws://[::1]:1234",
        "wss://example.invalid/api",
        "ws://example.invalid/api",
    ] {
        let config: Remote = serde_json::from_value(json!({"url":url})).unwrap();
        config.validate().unwrap();
        let expected = config.fingerprint().unwrap();
        let runtime = config.runtime(Default::default());
        assert_eq!(runtime.fingerprint().unwrap(), expected);
        assert!(!runtime.supports_recovery());
        assert!(
            runtime
                .validate_cwd(std::path::Path::new("/MASS-remote-only-missing-directory"))
                .is_ok()
        );
        assert!(
            runtime
                .validate_cwd(std::path::Path::new("relative"))
                .is_err()
        );
        let error = runtime
            .validate_recovery(
                "arbitrary-real-agent-id",
                &json!({}),
                std::path::Path::new("/remote"),
            )
            .unwrap_err();
        assert_eq!(
            error
                .downcast_ref::<mass_runtime::LifecycleError>()
                .unwrap()
                .kind,
            "unsupported_capability"
        );
    }
    for (field, value) in [
        ("command", json!("/local")),
        ("args", json!([])),
        ("env", json!({})),
        ("secretRefs", json!({})),
        ("sha256", json!("a".repeat(64))),
        ("unknown", json!(true)),
    ] {
        let mut invalid = valid.clone();
        invalid[field] = value;
        assert!(
            serde_json::from_value::<Remote>(invalid).is_err(),
            "{field}"
        );
    }
    for url in [
        "https://example.invalid",
        "unix:///tmp/no",
        "ws://user:canary@example.invalid",
        "ws://example.invalid/?token=canary",
        "wss://example.invalid/#secret",
    ] {
        let config: Remote = serde_json::from_value(json!({"url":url})).unwrap();
        let error = config.validate().unwrap_err().to_string();
        assert!(!error.contains("canary"));
    }
    for (field, value) in [
        ("closeTimeoutMs", json!(0)),
        ("factLaneCapacity", json!(65)),
        ("sdkEventBytes", json!(1023)),
        ("authRef", json!("literal-token")),
    ] {
        let mut invalid = valid.clone();
        invalid[field] = value;
        assert!(
            serde_json::from_value::<Remote>(invalid)
                .unwrap()
                .validate()
                .is_err()
        );
    }
    for url in [
        "wss://example.invalid",
        "ws://localhost:1234",
        "ws://127.2.3.4:1234",
        "ws://[::1]:1234",
    ] {
        serde_json::from_value::<Remote>(json!({"url":url,"authRef":"env:AUTH_SOURCE"}))
            .unwrap()
            .validate()
            .unwrap();
    }
    assert!(
        serde_json::from_value::<Remote>(
            json!({"url":"ws://example.invalid","authRef":"env:AUTH_SOURCE"})
        )
        .unwrap()
        .validate()
        .is_err()
    );
}

#[tokio::test]
async fn remote_missing_auth_reference_fails_without_connection_or_secret_disclosure() {
    let config: mass_codex::RemoteRegistrationConfig = serde_json::from_value(
        json!({"url":"ws://127.0.0.1:1","authRef":"env:MASS_TICKET06_DEFINITELY_MISSING"}),
    )
    .unwrap();
    let runtime = config.runtime(Default::default());
    assert_eq!(runtime.status().await["phase"], "pending");
    let error = runtime.start().await.err().unwrap().to_string();
    assert_eq!(error, "Remote authentication reference unavailable");
    assert_eq!(runtime.status().await["phase"], "error");
    runtime.stop().await;
    assert_eq!(runtime.status().await["phase"], "stopped");
}
