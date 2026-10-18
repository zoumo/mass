//! HTTP JSON-RPC and live-only SSE transport over the session Interface.
use axum::{
    Json, Router,
    extract::State,
    routing::{get, post},
};
use axum::{
    http::StatusCode,
    response::{IntoResponse, Response},
};
use mass_sessions::RpcError;
use mass_sessions::Service;
use serde::Serialize;
use serde_json::{Value, json};
use std::sync::Arc;

pub fn router(service: Arc<Service>) -> Router {
    Router::new()
        .route("/livez", get(liveness))
        .route("/readyz", get(readiness))
        .route("/api/v1/agentd/jsonrpc", post(rpc))
        .with_state(service)
}

#[derive(Serialize)]
struct Liveness {
    status: &'static str,
}

#[derive(Serialize)]
struct Readiness {
    status: &'static str,
    agents: AgentAvailability,
}

#[derive(Serialize)]
struct AgentAvailability {
    configured: usize,
    connected: usize,
}

async fn liveness() -> Json<Liveness> {
    Json(Liveness { status: "alive" })
}

async fn readiness(State(service): State<Arc<Service>>) -> (StatusCode, Json<Readiness>) {
    let (configured, connected) = service.availability().await;
    let ready = service.ready().await;
    (
        if ready {
            StatusCode::OK
        } else {
            StatusCode::SERVICE_UNAVAILABLE
        },
        Json(Readiness {
            status: if ready { "ready" } else { "unavailable" },
            agents: AgentAvailability {
                configured,
                connected,
            },
        }),
    )
}

pub async fn rpc(State(service): State<Arc<Service>>, Json(request): Json<Value>) -> Response {
    let id = request.get("id").cloned().unwrap_or(Value::Null);
    if !request.is_object()
        || request["jsonrpc"] != "2.0"
        || !request["method"].is_string()
        || !(id.is_null() || id.is_string() || id.is_number())
    {
        return Json(
            json!({"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request"}}),
        )
        .into_response();
    }
    if request["method"] == "agentd/session/watch_event" && request.get("id").is_some() {
        return match service
            .watch(request.get("params").cloned().unwrap_or(json!({})))
            .await
        {
            Ok(receiver) => {
                let stopped = service.stopped();
                let stream = futures_util::stream::unfold(
                    (receiver, stopped),
                    |(mut receiver, mut stopped)| async move {
                        let value = loop {
                            // At shutdown, publish already committed queued events before EOF.
                            if *stopped.borrow() {
                                break receiver.try_recv().ok();
                            }
                            tokio::select! {
                                value=receiver.recv()=>break value.ok(),
                                _=stopped.changed()=>{},
                            }
                        }?;
                        Some((
                            Ok::<_, std::convert::Infallible>(
                                axum::response::sse::Event::default()
                                    .data(value.value().to_string()),
                            ),
                            (receiver, stopped),
                        ))
                    },
                );
                axum::response::Sse::new(stream)
                    .keep_alive(axum::response::sse::KeepAlive::default())
                    .into_response()
            }
            Err(error) => {
                Json(json!({"jsonrpc":"2.0","id":id,"error":error.response()})).into_response()
            }
        };
    }
    let method = request["method"].as_str().unwrap().to_owned();
    let params = request.get("params").cloned().unwrap_or(json!({}));
    // Independently own lifecycle work after the HTTP caller disappears.
    let result = tokio::spawn(async move { service.call(&method, params).await })
        .await
        .unwrap_or_else(|error| Err(RpcError::Internal(error.into())));
    if request.get("id").is_none() {
        return StatusCode::NO_CONTENT.into_response();
    }
    Json(match result {
        Ok(result) => json!({"jsonrpc":"2.0","id":id,"result":result}),
        Err(error) => json!({"jsonrpc":"2.0","id":id,"error":error.response()}),
    })
    .into_response()
}
