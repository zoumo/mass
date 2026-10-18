use serde_json::json;
use std::time::Duration;

#[tokio::test]
async fn relative_data_directory_is_resolved_against_config_parent_without_activation() {
    let root = tempfile::tempdir().unwrap();
    let config = root.path().join("config.json");
    std::fs::write(&config,json!({"dataDir":"relative-data","runtimes":[{"name":"configured","type":"codex","config":{"command":"/nonexistent","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}]}).to_string()).unwrap();
    let service = mass_daemon::load_service(
        Some(config),
        vec![],
        Duration::from_secs(1),
        Default::default(),
    )
    .unwrap();
    assert!(root.path().join("relative-data/sessions").is_dir());
    assert_eq!(service.availability().await, (1, 0));
    assert_eq!(
        service
            .call("agentd/session/list", json!({}))
            .await
            .unwrap(),
        json!([])
    );
    service.shutdown().await;
}

#[test]
fn unknown_configuration_fields_are_rejected_without_creating_data() {
    let root = tempfile::tempdir().unwrap();
    let config = root.path().join("config.json");
    std::fs::write(
        &config,
        json!({"dataDir":root.path().join("data"),"typo":true}).to_string(),
    )
    .unwrap();
    assert!(
        mass_daemon::load_service(
            Some(config),
            vec![],
            Duration::from_secs(1),
            Default::default()
        )
        .is_err()
    );
    assert!(!root.path().join("data").exists());
}

fn runtime(name: &str) -> serde_json::Value {
    json!({"name":name,"type":"codex","config":{"command":"/never-started","sha256":"a".repeat(64),"env":{"PRIVATE_VALUE":"redaction-canary"},"secretRefs":{"TOKEN":"env:MASS_TEST_MISSING_SECRET"}}})
}
fn load(path: &std::path::Path) -> anyhow::Result<mass_sessions::Service> {
    mass_daemon::load_service(
        Some(path.into()),
        vec![],
        Duration::from_secs(1),
        Default::default(),
    )
}
fn write_config(path: &std::path::Path, runtimes: serde_json::Value) {
    std::fs::write(
        path,
        json!({"dataDir":"data","runtimes":runtimes}).to_string(),
    )
    .unwrap();
}

#[test]
fn strict_registration_rejects_invalid_files_before_creating_data() {
    let valid = runtime("one");
    let mut cases = vec![json!([valid.clone(), valid.clone()])];
    for (pointer, value) in [
        ("/type", json!("codex-remote")),
        ("/type", json!("claude")),
        ("/type", json!("acp")),
        ("/name", json!("../bad")),
        ("/name", json!("")),
        ("/config/command", json!("relative")),
        ("/config/sha256", json!("bad")),
    ] {
        let mut invalid = valid.clone();
        *invalid.pointer_mut(pointer).unwrap() = value;
        cases.push(json!([invalid]));
    }
    for (field, value) in [
        ("provider", json!("codex")),
        ("executable", json!("/old")),
        ("extra", json!(true)),
        ("closeTimeoutMs", json!(0)),
        ("factLaneCapacity", json!(65)),
        ("sdkEventBytes", json!(1023)),
        ("args", json!(["--listen", "ws://evil"])),
        ("secretRefs", json!({"PRIVATE_VALUE":"env:SECRET"})),
        ("secretRefs", json!({"TOKEN":"plaintext"})),
    ] {
        let mut invalid = valid.clone();
        invalid["config"][field] = value;
        cases.push(json!([invalid]));
    }
    let mut invalid = valid.clone();
    invalid["status"] = json!({"phase":"ready"});
    cases.push(json!([invalid]));
    for runtimes in cases {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("config.json");
        write_config(&path, runtimes);
        assert!(load(&path).is_err());
        assert!(!root.path().join("data").exists());
    }
    for old in [json!({"agents":{}}), json!({"provider":"codex"})] {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("config.json");
        std::fs::write(&path, old.to_string()).unwrap();
        assert!(load(&path).is_err());
    }
}

#[tokio::test]
async fn durable_registration_omission_restart_and_config_references() {
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("config.json");
    let registry = root.path().join("data/runtime-registry.json");
    write_config(&path, json!([runtime("one"), runtime("two")]));
    let service = load(&path).unwrap();
    let list = service
        .call("agentd/runtime/list", json!({}))
        .await
        .unwrap();
    assert_eq!(list.as_array().unwrap().len(), 2);
    assert_eq!(list[0]["status"]["phase"], "pending");
    assert!(!list.to_string().contains("redaction-canary"));
    assert!(!list.to_string().contains("/never-started"));
    assert!(service.ready().await);
    let initial: serde_json::Value =
        serde_json::from_slice(&std::fs::read(&registry).unwrap()).unwrap();
    assert_ne!(
        initial["records"]["one"]["identity"],
        initial["records"]["two"]["identity"]
    );
    assert!(
        load(&path).is_err(),
        "concurrent daemon must not replace ownership"
    );
    service.shutdown().await;
    drop(service);
    write_config(&path, json!([]));
    let service = load(&path).unwrap();
    assert!(service.ready().await);
    drop(service);
    assert_eq!(
        serde_json::from_slice::<serde_json::Value>(&std::fs::read(&registry).unwrap()).unwrap(),
        initial
    );
    write_config(&path, json!([runtime("one")]));
    let service = load(&path).unwrap();
    drop(service);
    let binding =
        json!({"identity":initial["records"]["one"]["identity"],"type":"codex","version":1});
    let id = "018f0000-0000-7000-8000-000000000001";
    let metadata=serde_json::from_value(json!({"sessionName":"saved","sessionId":id,"formatVersion":2,"instance":binding,"recovery":{},"historyMode":"paginated","spec":{"runtimeName":"one","cwd":root.path()},"configOptions":[],"capabilities":{}})).unwrap();
    mass_journal::save(&root.path().join("data"), &metadata).unwrap();
    let metadata_path =
        mass_journal::directory(&root.path().join("data"), &metadata).join("metadata.json");
    let saved = std::fs::read(&metadata_path).unwrap();
    let service = load(&path).unwrap();
    assert_eq!(
        service
            .call("agentd/session/list", json!({}))
            .await
            .unwrap()[0]["connected"],
        false
    );
    drop(service);
    let mut corrupted: serde_json::Value = serde_json::from_slice(&saved).unwrap();
    corrupted["instance"]["identity"] = json!("018f0000-0000-7000-8000-000000000099");
    let corrupted = serde_json::to_vec(&corrupted).unwrap();
    std::fs::write(&metadata_path, &corrupted).unwrap();
    assert!(
        load(&path).is_err(),
        "a name cannot replace a different persisted identity"
    );
    assert_eq!(std::fs::read(&metadata_path).unwrap(), corrupted);
    std::fs::write(&metadata_path, &saved).unwrap();
    let mut changed = runtime("one");
    changed["config"]["command"] = json!("/replacement");
    write_config(&path, json!([changed]));
    assert!(load(&path).is_err());
    assert_eq!(std::fs::read(&metadata_path).unwrap(), saved);
    assert_eq!(
        serde_json::from_slice::<serde_json::Value>(&std::fs::read(&registry).unwrap()).unwrap(),
        initial
    );
    write_config(&path, json!([]));
    let service = load(&path).unwrap();
    drop(service);
    write_config(&path, json!([runtime("one")]));
    let service = load(&path).unwrap();
    drop(service);
    std::fs::remove_file(&registry).unwrap();
    assert!(
        load(&path).is_err(),
        "missing identity cannot claim old session"
    );
    assert!(!registry.exists());
    assert_eq!(std::fs::read(metadata_path).unwrap(), saved);
}

#[test]
fn corrupt_identity_fails_without_repair_and_unreferenced_changes_increment_version() {
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("config.json");
    write_config(&path, json!([runtime("one")]));
    drop(load(&path).unwrap());
    let registry = root.path().join("data/runtime-registry.json");
    let initial: serde_json::Value =
        serde_json::from_slice(&std::fs::read(&registry).unwrap()).unwrap();
    let mut changed = runtime("one");
    changed["config"]["command"] = json!("/changed");
    write_config(&path, json!([changed]));
    drop(load(&path).unwrap());
    let updated: serde_json::Value =
        serde_json::from_slice(&std::fs::read(&registry).unwrap()).unwrap();
    assert_eq!(
        updated["records"]["one"]["identity"],
        initial["records"]["one"]["identity"]
    );
    assert_eq!(updated["records"]["one"]["version"], 2);
    std::fs::write(&registry, b"{broken").unwrap();
    assert!(load(&path).is_err());
    assert_eq!(std::fs::read(registry).unwrap(), b"{broken");
}

#[tokio::test]
async fn remote_registration_is_lazy_redacted_and_rejects_local_launch_configuration() {
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("config.json");
    let remote = json!({"name":"remote","type":"codex-remote","config":{"url":"wss://example.invalid","authRef":"env:MASS_UNUSED_REMOTE_AUTH"}});
    write_config(&path, json!([remote.clone()]));
    let service = load(&path).unwrap();
    let listed = service
        .call("agentd/runtime/list", json!({}))
        .await
        .unwrap();
    assert_eq!(listed[0]["type"], "codex-remote");
    assert_eq!(listed[0]["status"]["phase"], "pending");
    assert!(!listed.to_string().contains("example.invalid"));
    assert!(!listed.to_string().contains("MASS_UNUSED_REMOTE_AUTH"));
    service.shutdown().await;
    for field in ["command", "args", "env", "secretRefs", "sha256"] {
        let mut invalid = remote.clone();
        invalid["config"][field] = json!("canary-secret");
        write_config(&path, json!([invalid]));
        let error = load(&path).err().unwrap().to_string();
        assert!(!error.contains("canary-secret"));
    }
}
