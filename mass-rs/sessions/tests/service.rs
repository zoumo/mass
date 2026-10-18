use mass_codex::{Observation, ProviderRuntime};
use mass_sessions::Service;
use serde_json::{Value, json};
use std::{collections::BTreeMap, sync::Arc, time::Duration};

fn providers() -> BTreeMap<String, Arc<ProviderRuntime>> {
    BTreeMap::from([(
        "codex".into(),
        Arc::new(ProviderRuntime::new(
            serde_json::from_value(json!({
        "provider":"codex","executable":"/missing-mass-test-binary","sha256":"x"}))
            .unwrap(),
            Observation::default(),
        )),
    )])
}
fn service(dir: &std::path::Path) -> Service {
    Service::new(dir.into(), providers(), vec![], Duration::from_millis(100)).unwrap()
}
async fn call(service: &Service, method: &str, params: Value) -> Value {
    service
        .call(&format!("agentd/{method}"), params)
        .await
        .unwrap()
}

#[tokio::test]
async fn failed_creation_releases_name_and_provisional_files_without_activating_handlers() {
    let dir = tempfile::tempdir().unwrap();
    let service = service(dir.path());
    for _ in 0..2 {
        let result = service
            .call(
                "agentd/session/new",
                json!({"sessionName":"retry","spec":{"runtimeName":"codex","cwd":dir.path()}}),
            )
            .await
            .unwrap_err()
            .response();
        assert_eq!(
            result["code"], -32603,
            "name reservation must have been released: {result}"
        );
        assert_eq!(
            std::fs::read_dir(dir.path().join("sessions"))
                .unwrap()
                .count(),
            0
        );
        assert_eq!(call(&service, "session/list", json!({})).await, json!([]));
    }
    service.shutdown().await;
}

#[tokio::test]
async fn invalid_creation_and_live_only_watch_fail_before_native_activation() {
    let dir = tempfile::tempdir().unwrap();
    let service = service(dir.path());
    for (name, cwd, runtime) in [
        (" ", dir.path().to_path_buf(), "codex"),
        ("test", "relative".into(), "codex"),
        ("test", dir.path().into(), "unknown"),
    ] {
        assert_eq!(
            service
                .call(
                    "agentd/session/new",
                    json!({"sessionName":name,"spec":{"runtimeName":runtime,"cwd":cwd}})
                )
                .await
                .unwrap_err()
                .response()["code"],
            -32602
        );
    }
    assert_eq!(service.call("agentd/session/new", json!({"sessionName":"preset","sessionId":"client-id","spec":{"runtimeName":"codex","cwd":dir.path()}})).await.unwrap_err().response()["code"], -32602);
    for key in ["afterSeq", "lastSeq", "replayFrom"] {
        let mut params = json!({"sessionName":"test"});
        params[key] = json!(0);
        let error = service.watch(params).await.err().unwrap().response();
        assert_eq!(error["message"], "watch_event is live-only");
    }
    assert_eq!(service.availability().await, (1, 0));
    service.shutdown().await;
}

#[tokio::test]
async fn offline_queries_preserve_history_and_identity_without_opening_configured_runtime() {
    let dir = tempfile::tempdir().unwrap();
    let id = "018f0000-0000-7000-8000-000000000001";
    let initialized = service(dir.path());
    drop(initialized);
    let ledger: Value =
        serde_json::from_slice(&std::fs::read(dir.path().join("runtime-registry.json")).unwrap())
            .unwrap();
    let binding =
        json!({"identity":ledger["records"]["codex"]["identity"],"type":"codex","version":1});
    let metadata = serde_json::from_value(json!({"sessionName":"saved","sessionId":id,
        "formatVersion":2,"instance":binding,"recovery":{},
        "historyMode":"paginated","spec":{"runtimeName":"codex","cwd":dir.path()},"configOptions":[],"capabilities":{}})).unwrap();
    mass_journal::save(dir.path(), &metadata).unwrap();
    let path = mass_journal::directory(dir.path(), &metadata).join("events.jsonl");
    for seq in 1..=3 {
        mass_journal::append_record(&path, &json!({"event":{"schemaVersion":2,"sessionName":"saved","sessionId":id,"time":"2026-10-17T00:00:00Z","seq":seq,"type":"agent.message","itemId":"opaque-item","payload":{"messageId":"opaque-item","content":[]}},"evidence":{"version":1}})).unwrap();
    }
    let original = std::fs::read(&path).unwrap();
    let service = service(dir.path());
    let status = call(&service, "session/status", json!({"sessionName":"saved"})).await;
    assert_eq!(status["sessionId"], id);
    assert_eq!(status["connected"], false);
    assert_eq!(status["lifecycle"], "offline");
    assert!(status["state"].is_null());
    assert!(status["configOptions"].is_null());
    assert_eq!(status["capabilities"]["prompt"], false);
    assert_eq!(status["capabilities"]["resume"], true);
    let page = call(
        &service,
        "session/get_event",
        json!({"sessionName":"saved","afterSeq":1,"limit":1}),
    )
    .await;
    assert_eq!(
        page,
        json!({"events":[{"schemaVersion":2,"sessionName":"saved","sessionId":id,"time":"2026-10-17T00:00:00Z","seq":2,"type":"agent.message","itemId":"opaque-item","payload":{"messageId":"opaque-item","content":[]}}],"nextSeq":2})
    );
    for limit in [0, 1001] {
        assert_eq!(
            service
                .call(
                    "agentd/session/get_event",
                    json!({"sessionName":"saved","limit":limit})
                )
                .await
                .unwrap_err()
                .response()["code"],
            -32602
        );
    }
    assert_eq!(
        call(&service, "session/archive", json!({"sessionName":"saved"})).await["connected"],
        false
    );
    assert_eq!(service.availability().await, (1, 0));
    assert_eq!(std::fs::read(path).unwrap(), original);
    service.shutdown().await;
}

#[tokio::test]
async fn shutdown_is_observable_and_rejects_new_lifecycle_work_but_keeps_read_queries() {
    let dir = tempfile::tempdir().unwrap();
    let service = service(dir.path());
    let stopped = service.stopped();
    assert!(!*stopped.borrow());
    service.shutdown().await;
    service.shutdown().await;
    assert!(*stopped.borrow());
    assert!(
        service
            .call(
                "agentd/session/new",
                json!({"sessionName":"test","spec":{"runtimeName":"codex","cwd":dir.path()}})
            )
            .await
            .is_err()
    );
    assert_eq!(call(&service, "session/list", json!({})).await, json!([]));
    assert_eq!(
        service
            .call("agentd/session/respond", json!({"answer":"yes"}))
            .await
            .unwrap_err()
            .response()["code"],
        -32601
    );
}
