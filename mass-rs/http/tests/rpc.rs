use axum::{Json, body::to_bytes, extract::State};
use mass_sessions::Service;
use serde_json::{Value, json};
use std::{collections::BTreeMap, sync::Arc, time::Duration};

async fn request(service: Arc<Service>, value: Value) -> (u16, Value) {
    let response = mass_http::rpc(State(service), Json(value)).await;
    let status = response.status().as_u16();
    let bytes = to_bytes(response.into_body(), 1024 * 1024).await.unwrap();
    (
        status,
        if bytes.is_empty() {
            Value::Null
        } else {
            serde_json::from_slice(&bytes).unwrap()
        },
    )
}

#[tokio::test]
async fn malformed_json_rpc_envelopes_return_invalid_request_with_null_id() {
    let dir = tempfile::tempdir().unwrap();
    let service = Arc::new(
        Service::with_runtimes(
            dir.path().into(),
            BTreeMap::new(),
            vec![],
            vec![],
            Duration::from_secs(1),
        )
        .unwrap(),
    );
    for value in [
        json!([]),
        Value::Null,
        json!({"method":"agentd/status"}),
        json!({"jsonrpc":"2.0","method":7}),
        json!({"jsonrpc":"2.0","method":"agentd/status","id":true}),
    ] {
        let (status, response) = request(service.clone(), value).await;
        assert_eq!(status, 200);
        assert_eq!(response["error"]["code"], -32600);
        assert!(response["id"].is_null());
    }
    service.shutdown().await;
}

#[tokio::test]
async fn ids_notifications_and_errors_follow_json_rpc_contract() {
    let dir = tempfile::tempdir().unwrap();
    let service = Arc::new(
        Service::with_runtimes(
            dir.path().into(),
            BTreeMap::new(),
            vec![],
            vec![],
            Duration::from_secs(1),
        )
        .unwrap(),
    );
    for id in [json!(42), json!("caller"), Value::Null] {
        let (_, response) = request(
            service.clone(),
            json!({"jsonrpc":"2.0","id":id,"method":"agentd/status"}),
        )
        .await;
        assert_eq!(response["id"], id);
        assert_eq!(response["result"]["connected"], 0);
    }
    assert_eq!(
        request(
            service.clone(),
            json!({"jsonrpc":"2.0","method":"agentd/status"})
        )
        .await,
        (204, Value::Null)
    );
    for (method, params, code) in [
        ("unknown", json!({}), -32601),
        ("agentd/session/status", json!({}), -32602),
        (
            "agentd/session/watch_event",
            json!({"sessionName":"missing","afterSeq":0}),
            -32602,
        ),
    ] {
        let (_, response) = request(
            service.clone(),
            json!({"jsonrpc":"2.0","id":"err","method":method,"params":params}),
        )
        .await;
        assert_eq!(response["id"], "err");
        assert_eq!(response["error"]["code"], code);
    }
    service.shutdown().await;
}
