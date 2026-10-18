/*
This module implements the remote app-server client transport.

It owns the remote connection lifecycle, including the initialize/initialized
handshake, JSON-RPC request/response routing, server-request resolution, and
notification streaming. Remote connections always carry WebSocket frames, over
either TCP WebSocket URLs or local Unix sockets. The rest of the crate uses the
same `AppServerEvent` surface for both in-process and remote transports, so
callers such as the TUI can switch between them without changing their
higher-level session logic.
*/

include!("notification_methods.rs");
use std::collections::HashMap;
use std::collections::VecDeque;
use std::io::Error as IoError;
use std::io::ErrorKind;
use std::io::Result as IoResult;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::{OwnedSemaphorePermit, Semaphore};

use crate::AppServerEvent;
use crate::RequestResult;
use crate::SHUTDOWN_TIMEOUT;
use crate::TypedRequestError;
use crate::decode_typed_response;
use codex_app_server_protocol::ClientInfo;
use codex_app_server_protocol::ClientNotification;
use codex_app_server_protocol::ClientRequest;
use codex_app_server_protocol::InitializeCapabilities;
use codex_app_server_protocol::InitializeParams;
use codex_app_server_protocol::JSONRPCError;
use codex_app_server_protocol::JSONRPCErrorError;
use codex_app_server_protocol::JSONRPCMessage;
use codex_app_server_protocol::JSONRPCNotification;
use codex_app_server_protocol::JSONRPCRequest;
use codex_app_server_protocol::JSONRPCResponse;
use codex_app_server_protocol::RequestId;
use codex_app_server_protocol::Result as JsonRpcResult;
use codex_app_server_protocol::ServerNotification;
use codex_app_server_protocol::ServerRequest;
use codex_uds::UnixStream;
use codex_utils_absolute_path::AbsolutePathBuf;
use codex_utils_rustls_provider::ensure_rustls_crypto_provider;
use futures::SinkExt;
use futures::StreamExt;
use serde::de::DeserializeOwned;
use tokio::io::AsyncRead;
use tokio::io::AsyncWrite;
use tokio::net::TcpStream;
use tokio::sync::mpsc;
use tokio::sync::oneshot;
use tokio::time::timeout;
use tokio_tungstenite::MaybeTlsStream;
use tokio_tungstenite::WebSocketStream;
use tokio_tungstenite::client_async_with_config;
use tokio_tungstenite::connect_async_with_config;
use tokio_tungstenite::tungstenite::Error as TungsteniteError;
use tokio_tungstenite::tungstenite::Message;
use tokio_tungstenite::tungstenite::client::IntoClientRequest;
use tokio_tungstenite::tungstenite::http::HeaderValue;
use tokio_tungstenite::tungstenite::http::header::AUTHORIZATION;
use tokio_tungstenite::tungstenite::protocol::WebSocketConfig;
use tracing::warn;
use url::Url;

// MASS patch: queued and consumer in-flight typed events share these permits.
const REMOTE_EVENT_BYTES: usize = 32 << 20;
const REMOTE_EVENT_COUNT: usize = 32;

fn encoded_len<T: serde::Serialize + ?Sized>(value: &T) -> IoResult<usize> {
    struct Counter(usize);
    impl std::io::Write for Counter {
        fn write(&mut self, bytes: &[u8]) -> IoResult<usize> {
            self.0 += bytes.len();
            Ok(bytes.len())
        }
        fn flush(&mut self) -> IoResult<()> {
            Ok(())
        }
    }
    let mut counter = Counter(0);
    serde_json::to_writer(&mut counter, value).map_err(IoError::other)?;
    Ok(counter.0)
}

struct BufferedEvent {
    event: AppServerEvent,
    _count: OwnedSemaphorePermit,
    _bytes: OwnedSemaphorePermit,
}
#[derive(Clone)]
struct EventBudget {
    count: Arc<Semaphore>,
    bytes: Arc<Semaphore>,
    limit: usize,
    capacity: usize,
    high_count: Arc<std::sync::atomic::AtomicUsize>,
    high_bytes: Arc<std::sync::atomic::AtomicUsize>,
}
impl EventBudget {
    fn new(capacity: usize, limit: usize) -> Self {
        Self {
            count: Arc::new(Semaphore::new(capacity)),
            bytes: Arc::new(Semaphore::new(limit)),
            limit,
            capacity,
            high_count: Arc::new(std::sync::atomic::AtomicUsize::new(0)),
            high_bytes: Arc::new(std::sync::atomic::AtomicUsize::new(0)),
        }
    }
    fn size(&self, event: &AppServerEvent) -> IoResult<usize> {
        let bytes = match event {
            AppServerEvent::ServerNotification(value) => encoded_len(value)?,
            AppServerEvent::ServerRequest(value) => encoded_len(value)?,
            AppServerEvent::Disconnected { message } => encoded_len(message)?,
            AppServerEvent::UnsupportedNotification { method } => encoded_len(method)?,
            AppServerEvent::NotificationDecodeError {
                method,
                thread_id,
                message,
            } => encoded_len(&(method, thread_id, message))?,
            AppServerEvent::Lagged { skipped } => encoded_len(skipped)?,
        };
        if bytes > self.limit {
            return Err(IoError::new(
                ErrorKind::InvalidData,
                format!(
                    "remote event exceeds {} byte consumer budget; history incomplete",
                    self.limit
                ),
            ));
        }
        Ok(bytes)
    }
    fn record_high_water(&self) {
        self.high_count.fetch_max(
            self.capacity - self.count.available_permits(),
            std::sync::atomic::Ordering::Relaxed,
        );
        self.high_bytes.fetch_max(
            self.limit - self.bytes.available_permits(),
            std::sync::atomic::Ordering::Relaxed,
        );
    }
    fn initial(&self, event: AppServerEvent) -> IoResult<BufferedEvent> {
        if let AppServerEvent::Disconnected { message }
        | AppServerEvent::NotificationDecodeError { message, .. } = &event
        {
            return Err(IoError::new(ErrorKind::InvalidData, message.clone()));
        }
        let bytes = self.size(&event)?;
        let buffered = BufferedEvent {
            event,
            _count: self
                .count
                .clone()
                .try_acquire_owned()
                .map_err(IoError::other)?,
            _bytes: self
                .bytes
                .clone()
                .try_acquire_many_owned(bytes as u32)
                .map_err(IoError::other)?,
        };
        self.record_high_water();
        Ok(buffered)
    }
}
struct EventSender {
    sender: mpsc::Sender<BufferedEvent>,
    budget: EventBudget,
}

// A shutdown future may be cancelled by its owner. Dropping its guard must
// abort the official worker rather than detaching its JoinHandle.
struct AbortOnDrop(tokio::task::JoinHandle<()>);
impl std::ops::Deref for AbortOnDrop {
    type Target = tokio::task::JoinHandle<()>;
    fn deref(&self) -> &Self::Target {
        &self.0
    }
}
impl std::ops::DerefMut for AbortOnDrop {
    fn deref_mut(&mut self) -> &mut Self::Target {
        &mut self.0
    }
}
impl std::future::Future for AbortOnDrop {
    type Output = Result<(), tokio::task::JoinError>;
    fn poll(
        mut self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Self::Output> {
        std::pin::Pin::new(&mut self.0).poll(cx)
    }
}
impl Drop for AbortOnDrop {
    fn drop(&mut self) {
        self.0.abort();
    }
}

const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
const INITIALIZE_TIMEOUT: Duration = Duration::from_secs(10);
const REMOTE_APP_SERVER_MAX_WEBSOCKET_MESSAGE_SIZE: usize = 128 << 20;
// Tungstenite still needs an HTTP request URI for the WebSocket handshake;
// the bytes travel over the Unix socket, not TCP.
const UDS_WEBSOCKET_HANDSHAKE_URL: &str = "ws://localhost/rpc";

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RemoteAppServerEndpoint {
    WebSocket {
        websocket_url: String,
        auth_token: Option<String>,
    },
    UnixSocket {
        socket_path: AbsolutePathBuf,
    },
}

#[derive(Debug, Clone)]
pub struct RemoteAppServerConnectArgs {
    pub endpoint: RemoteAppServerEndpoint,
    pub client_name: String,
    pub client_version: String,
    pub experimental_api: bool,
    pub mcp_server_openai_form_elicitation: bool,
    pub opt_out_notification_methods: Vec<String>,
    pub channel_capacity: usize,
    /// MASS patch: queued + consumer in-flight serialized event bytes (1 KiB..32 MiB).
    pub event_byte_capacity: usize,
    /// Maximum serialized RPC result/error bytes; the WebSocket frame limit remains separate.
    pub max_response_bytes: usize,
}
impl RemoteAppServerConnectArgs {
    pub(crate) fn initialize_params(&self) -> InitializeParams {
        let capabilities = InitializeCapabilities {
            explicit_gateway_oauth: false,
            experimental_api: self.experimental_api,
            request_attestation: false,
            extensions: None,
            opt_out_notification_methods: if self.opt_out_notification_methods.is_empty() {
                None
            } else {
                Some(self.opt_out_notification_methods.clone())
            },
            mcp_server_openai_form_elicitation: self.mcp_server_openai_form_elicitation,
        };

        InitializeParams {
            client_info: ClientInfo {
                name: self.client_name.clone(),
                title: None,
                version: self.client_version.clone(),
            },
            capabilities: Some(capabilities),
        }
    }
}

pub(crate) fn websocket_url_supports_auth_token(url: &Url) -> bool {
    match (url.scheme(), url.host()) {
        ("wss", Some(_)) => true,
        ("ws", Some(url::Host::Domain(domain))) => domain.eq_ignore_ascii_case("localhost"),
        ("ws", Some(url::Host::Ipv4(addr))) => addr.is_loopback(),
        ("ws", Some(url::Host::Ipv6(addr))) => addr.is_loopback(),
        _ => false,
    }
}

enum RemoteClientCommand {
    Request {
        request: Box<JSONRPCRequest>,
        response_tx: oneshot::Sender<IoResult<RequestResult>>,
    },
    Notify {
        notification: ClientNotification,
        response_tx: oneshot::Sender<IoResult<()>>,
    },
    ResolveServerRequest {
        request_id: RequestId,
        result: JsonRpcResult,
        response_tx: oneshot::Sender<IoResult<()>>,
    },
    RejectServerRequest {
        request_id: RequestId,
        error: JSONRPCErrorError,
        response_tx: oneshot::Sender<IoResult<()>>,
    },
    Shutdown {
        response_tx: oneshot::Sender<IoResult<()>>,
    },
}

#[derive(Default)]
struct RemoteServerMetadata {
    server_version: Option<String>,
    codex_home: Option<String>,
    platform_family: Option<String>,
    platform_os: Option<String>,
}

pub struct RemoteAppServerClient {
    command_tx: mpsc::Sender<RemoteClientCommand>,
    event_rx: mpsc::Receiver<BufferedEvent>,
    pending_events: VecDeque<BufferedEvent>,
    event_budget: EventBudget,
    in_flight: Option<(OwnedSemaphorePermit, OwnedSemaphorePermit)>,
    metadata: RemoteServerMetadata,
    worker_handle: AbortOnDrop,
}

#[derive(Clone)]
pub struct RemoteAppServerRequestHandle {
    command_tx: mpsc::Sender<RemoteClientCommand>,
}

enum SocketPeerPolicy {
    ExplicitEndpoint,
    #[cfg(windows)]
    NonElevatedCurrentUser,
}

impl RemoteAppServerClient {
    pub async fn connect(args: RemoteAppServerConnectArgs) -> IoResult<Self> {
        Self::connect_with_policy(args, SocketPeerPolicy::ExplicitEndpoint).await
    }

    /// Connects to an implicitly discovered Windows daemon, verifying its peer
    /// token before the WebSocket handshake or any session requests.
    #[cfg(windows)]
    pub async fn connect_local_daemon(args: RemoteAppServerConnectArgs) -> IoResult<Self> {
        if !matches!(args.endpoint, RemoteAppServerEndpoint::UnixSocket { .. }) {
            return Err(IoError::new(
                ErrorKind::InvalidInput,
                "local daemon requires a Unix socket",
            ));
        }
        Self::connect_with_policy(args, SocketPeerPolicy::NonElevatedCurrentUser).await
    }

    async fn connect_with_policy(
        args: RemoteAppServerConnectArgs,
        peer_policy: SocketPeerPolicy,
    ) -> IoResult<Self> {
        if !(1..=REMOTE_EVENT_COUNT).contains(&args.channel_capacity) {
            return Err(IoError::new(
                ErrorKind::InvalidInput,
                "remote channel capacity must be 1..32",
            ));
        }
        if !(1024..=REMOTE_EVENT_BYTES).contains(&args.event_byte_capacity) {
            return Err(IoError::new(
                ErrorKind::InvalidInput,
                "event byte capacity must be 1 KiB..32 MiB",
            ));
        }
        let channel_capacity = args.channel_capacity;
        let event_byte_capacity = args.event_byte_capacity;
        if !(1024..=REMOTE_APP_SERVER_MAX_WEBSOCKET_MESSAGE_SIZE).contains(&args.max_response_bytes)
        {
            return Err(IoError::new(
                ErrorKind::InvalidInput,
                "invalid RPC response byte limit",
            ));
        }
        let max_response_bytes = args.max_response_bytes;
        let initialize_params = args.initialize_params();
        match args.endpoint {
            RemoteAppServerEndpoint::WebSocket {
                websocket_url,
                auth_token,
            } => {
                let (endpoint, stream) =
                    connect_websocket_endpoint(websocket_url, auth_token).await?;
                Self::connect_with_stream(
                    channel_capacity,
                    event_byte_capacity,
                    max_response_bytes,
                    endpoint,
                    stream,
                    initialize_params,
                )
                .await
            }
            RemoteAppServerEndpoint::UnixSocket { socket_path } => {
                let (endpoint, stream) =
                    connect_unix_socket_endpoint(socket_path, peer_policy).await?;
                Self::connect_with_stream(
                    channel_capacity,
                    event_byte_capacity,
                    max_response_bytes,
                    endpoint,
                    stream,
                    initialize_params,
                )
                .await
            }
        }
    }

    pub fn server_version(&self) -> Option<&str> {
        self.metadata.server_version.as_deref()
    }

    pub fn codex_home(&self) -> Option<&str> {
        self.metadata.codex_home.as_deref()
    }

    pub fn platform_family(&self) -> Option<&str> {
        self.metadata.platform_family.as_deref()
    }

    pub fn platform_os(&self) -> Option<&str> {
        self.metadata.platform_os.as_deref()
    }

    async fn connect_with_stream<S>(
        channel_capacity: usize,
        event_byte_capacity: usize,
        max_response_bytes: usize,
        endpoint: String,
        stream: WebSocketStream<S>,
        initialize_params: InitializeParams,
    ) -> IoResult<Self>
    where
        S: AsyncRead + AsyncWrite + Unpin + Send + 'static,
    {
        let mut stream = stream;
        let budget = EventBudget::new(channel_capacity, event_byte_capacity);
        let (pending_events, metadata) = initialize_remote_connection(
            &mut stream,
            &endpoint,
            initialize_params,
            INITIALIZE_TIMEOUT,
            &budget,
            max_response_bytes,
        )
        .await?;

        let (command_tx, mut command_rx) = mpsc::channel::<RemoteClientCommand>(channel_capacity);
        let (sender, event_rx) = mpsc::channel::<BufferedEvent>(channel_capacity);
        let event_tx = EventSender {
            sender,
            budget: budget.clone(),
        };
        let worker_handle = tokio::spawn(async move {
            let mut pending_requests =
                HashMap::<RequestId, oneshot::Sender<IoResult<RequestResult>>>::new();
            let mut worker_exit_error: Option<(ErrorKind, String)> = None;
            loop {
                tokio::select! {
                    command = command_rx.recv() => {
                        let Some(command) = command else {
                            let _ = stream.close(None).await;
                            break;
                        };
                        match command {
                            RemoteClientCommand::Request { request, response_tx } => {
                                let request_id = request.id.clone();
                                if pending_requests.contains_key(&request_id) {
                                    let _ = response_tx.send(Err(IoError::new(
                                        ErrorKind::InvalidInput,
                                        format!("duplicate remote app-server request id `{request_id}`"),
                                    )));
                                    continue;
                                }
                                pending_requests.insert(request_id.clone(), response_tx);
                                if let Err(err) = write_jsonrpc_message(
                                    &mut stream,
                                    JSONRPCMessage::Request(*request),
                                    &endpoint,
                                )
                                .await
                                {
                                    let err_message = err.to_string();
                                    let message = format!(
                                        "remote app server at `{endpoint}` write failed: {err_message}"
                                    );
                                    if let Some(response_tx) = pending_requests.remove(&request_id) {
                                        let _ = response_tx.send(Err(err));
                                    }
                                    let _ = deliver_event(
                                        &event_tx,
                                        AppServerEvent::Disconnected {
                                            message: message.clone(),
                                        },
                                    ).await;
                                    worker_exit_error = Some((ErrorKind::BrokenPipe, message));
                                    break;
                                }
                            }
                            RemoteClientCommand::Notify { notification, response_tx } => {
                                let result = write_jsonrpc_message(
                                    &mut stream,
                                    JSONRPCMessage::Notification(
                                        jsonrpc_notification_from_client_notification(notification),
                                    ),
                                    &endpoint,
                                )
                                .await;
                                let _ = response_tx.send(result);
                            }
                            RemoteClientCommand::ResolveServerRequest {
                                request_id,
                                result,
                                response_tx,
                            } => {
                                let result = write_jsonrpc_message(
                                    &mut stream,
                                    JSONRPCMessage::Response(JSONRPCResponse {
                                        id: request_id,
                                        result,
                                    }),
                                    &endpoint,
                                )
                                .await;
                                let _ = response_tx.send(result);
                            }
                            RemoteClientCommand::RejectServerRequest {
                                request_id,
                                error,
                                response_tx,
                            } => {
                                let result = write_jsonrpc_message(
                                    &mut stream,
                                    JSONRPCMessage::Error(JSONRPCError {
                                        error,
                                        id: request_id,
                                    }),
                                    &endpoint,
                                )
                                .await;
                                let _ = response_tx.send(result);
                            }
                            RemoteClientCommand::Shutdown { response_tx } => {
                                let close_result = stream.close(None).await.or_else(|err| {
                                    if websocket_close_error_is_already_closed(&err) {
                                        Ok(())
                                    } else {
                                        Err(IoError::other(format!(
                                            "failed to close websocket app server `{endpoint}`: {err}"
                                        )))
                                    }
                                });
                                let _ = response_tx.send(close_result);
                                break;
                            }
                        }
                    }
                    message = stream.next() => {
                        match message {
                            Some(Ok(Message::Text(text))) => {
                                match serde_json::from_str::<JSONRPCMessage>(&text) {
                                    Ok(JSONRPCMessage::Response(response)) => {
                                        if encoded_len(&response.result).is_ok_and(|bytes|bytes>max_response_bytes) {
                                            let message=format!("RPC result exceeds {max_response_bytes} byte limit; outcome unknown");
                                            let _=deliver_event(&event_tx,AppServerEvent::Disconnected{message:message.clone()}).await;
                                            worker_exit_error=Some((ErrorKind::InvalidData,message));break;
                                        }
                                        if let Some(response_tx) = pending_requests.remove(&response.id) {
                                            let _ = response_tx.send(Ok(Ok(response.result)));
                                        }
                                    }
                                    Ok(JSONRPCMessage::Error(error)) => {
                                        if encoded_len(&error.error).is_ok_and(|bytes|bytes>max_response_bytes) {
                                            let message=format!("RPC error exceeds {max_response_bytes} byte limit; outcome unknown");
                                            let _=deliver_event(&event_tx,AppServerEvent::Disconnected{message:message.clone()}).await;
                                            worker_exit_error=Some((ErrorKind::InvalidData,message));break;
                                        }
                                        if let Some(response_tx) = pending_requests.remove(&error.id) {
                                            let _ = response_tx.send(Ok(Err(error.error)));
                                        }
                                    }
                                    Ok(JSONRPCMessage::Notification(notification)) => {
                                        if let Some(event) =
                                            app_server_event_from_notification(notification)
                                            && let Err(err) = deliver_event(
                                                &event_tx,
                                                event,
                                            ).await
                                            {
                                                warn!(%err, "failed to deliver remote app-server event");
                                                break;
                                            }
                                    }
                                    Ok(JSONRPCMessage::Request(request)) => {
                                        let request_id = request.id.clone();
                                        let method = request.method.clone();
                                        match ServerRequest::try_from(request) {
                                            Ok(request) => {
                                                if let Err(err) = deliver_event(
                                                    &event_tx,
                                                    AppServerEvent::ServerRequest(Box::new(request)),
                                                ).await
                                                {
                                                    warn!(%err, "failed to deliver remote app-server server request");
                                                    break;
                                                }
                                            }
                                            Err(err) => {
                                                warn!(%err, method, "rejecting unknown remote app-server request");
                                                if let Err(reject_err) = write_jsonrpc_message(
                                                    &mut stream,
                                                    JSONRPCMessage::Error(JSONRPCError {
                                                        error: JSONRPCErrorError {
                                                            code: -32601,
                                                            message: format!(
                                                                "unsupported remote app-server request `{method}`"
                                                            ),
                                                            data: None,
                                                        },
                                                        id: request_id,
                                                    }),
                                                    &endpoint,
                                                )
                                                .await
                                                {
                                                    let err_message = reject_err.to_string();
                                                    let message = format!(
                                                        "remote app server at `{endpoint}` write failed: {err_message}"
                                                    );
                                                    let _ = deliver_event(
                                                        &event_tx,
                                                        AppServerEvent::Disconnected {
                                                            message: message.clone(),
                                                        },
                                                    ).await;
                                                    worker_exit_error =
                                                        Some((ErrorKind::BrokenPipe, message));
                                                    break;
                                                }
                                            }
                                        }
                                    }
                                    Err(err) => {
                                        let message = format!(
                                            "remote app server at `{endpoint}` sent invalid JSON-RPC: {err}"
                                        );
                                        let _ = deliver_event(
                                            &event_tx,
                                            AppServerEvent::Disconnected {
                                                message: message.clone(),
                                            },
                                        ).await;
                                        worker_exit_error =
                                            Some((ErrorKind::InvalidData, message));
                                        break;
                                    }
                                }
                            }
                            Some(Ok(Message::Close(frame))) => {
                                let reason = frame
                                    .as_ref()
                                    .map(|frame| frame.reason.to_string())
                                    .filter(|reason| !reason.is_empty())
                                    .unwrap_or_else(|| "connection closed".to_string());
                                let message = format!(
                                    "remote app server at `{endpoint}` disconnected: {reason}"
                                );
                                let _ = deliver_event(
                                    &event_tx,
                                    AppServerEvent::Disconnected {
                                        message: message.clone(),
                                    },
                                ).await;
                                worker_exit_error = Some((
                                    ErrorKind::ConnectionAborted,
                                    message,
                                ));
                                break;
                            }
                            Some(Ok(Message::Binary(_)))=> {
                                let message="unsupported binary app-server frame".to_string();
                                let _=deliver_event(&event_tx,AppServerEvent::Disconnected{message:message.clone()}).await;
                                worker_exit_error=Some((ErrorKind::InvalidData,message));break;
                            }
                            Some(Ok(Message::Ping(_)))
                            | Some(Ok(Message::Pong(_)))
                            | Some(Ok(Message::Frame(_))) => {}
                            Some(Err(err)) => {
                                let message = format!(
                                    "remote app server at `{endpoint}` transport failed: {err}"
                                );
                                let _ = deliver_event(
                                    &event_tx,
                                    AppServerEvent::Disconnected {
                                        message: message.clone(),
                                    },
                                ).await;
                                worker_exit_error = Some((ErrorKind::InvalidData, message));
                                break;
                            }
                            None => {
                                let message = format!(
                                    "remote app server at `{endpoint}` closed the connection"
                                );
                                let _ = deliver_event(
                                    &event_tx,
                                    AppServerEvent::Disconnected {
                                        message: message.clone(),
                                    },
                                ).await;
                                worker_exit_error = Some((ErrorKind::UnexpectedEof, message));
                                break;
                            }
                        }
                    }
                }
            }

            let (err_kind, err_message) = worker_exit_error.unwrap_or_else(|| {
                (
                    ErrorKind::BrokenPipe,
                    "remote app-server worker channel is closed".to_string(),
                )
            });
            for (_, response_tx) in pending_requests {
                let _ = response_tx.send(Err(IoError::new(err_kind, err_message.clone())));
            }
        });

        Ok(Self {
            command_tx,
            event_rx,
            pending_events: pending_events.into(),
            event_budget: budget,
            in_flight: None,
            metadata,
            worker_handle: AbortOnDrop(worker_handle),
        })
    }

    pub fn request_handle(&self) -> RemoteAppServerRequestHandle {
        RemoteAppServerRequestHandle {
            command_tx: self.command_tx.clone(),
        }
    }

    pub async fn request(&self, request: ClientRequest) -> IoResult<RequestResult> {
        self.request_handle().request(request).await
    }

    pub async fn request_typed<T>(&self, request: ClientRequest) -> Result<T, TypedRequestError>
    where
        T: DeserializeOwned,
    {
        let method = request.method_name();
        let response = self.request(request).await;
        decode_typed_response(method, response)
    }

    pub async fn notify(&self, notification: ClientNotification) -> IoResult<()> {
        let (response_tx, response_rx) = oneshot::channel();
        self.command_tx
            .send(RemoteClientCommand::Notify {
                notification,
                response_tx,
            })
            .await
            .map_err(|_| {
                IoError::new(
                    ErrorKind::BrokenPipe,
                    "remote app-server worker channel is closed",
                )
            })?;
        response_rx.await.map_err(|_| {
            IoError::new(
                ErrorKind::BrokenPipe,
                "remote app-server notify channel is closed",
            )
        })?
    }

    pub async fn resolve_server_request(
        &self,
        request_id: RequestId,
        result: JsonRpcResult,
    ) -> IoResult<()> {
        let (response_tx, response_rx) = oneshot::channel();
        self.command_tx
            .send(RemoteClientCommand::ResolveServerRequest {
                request_id,
                result,
                response_tx,
            })
            .await
            .map_err(|_| {
                IoError::new(
                    ErrorKind::BrokenPipe,
                    "remote app-server worker channel is closed",
                )
            })?;
        response_rx.await.map_err(|_| {
            IoError::new(
                ErrorKind::BrokenPipe,
                "remote app-server resolve channel is closed",
            )
        })?
    }

    pub async fn reject_server_request(
        &self,
        request_id: RequestId,
        error: JSONRPCErrorError,
    ) -> IoResult<()> {
        let (response_tx, response_rx) = oneshot::channel();
        self.command_tx
            .send(RemoteClientCommand::RejectServerRequest {
                request_id,
                error,
                response_tx,
            })
            .await
            .map_err(|_| {
                IoError::new(
                    ErrorKind::BrokenPipe,
                    "remote app-server worker channel is closed",
                )
            })?;
        response_rx.await.map_err(|_| {
            IoError::new(
                ErrorKind::BrokenPipe,
                "remote app-server reject channel is closed",
            )
        })?
    }

    /// Serialized event payload usage, including the caller's current event.
    pub fn event_buffer_high_water(&self) -> (usize, usize) {
        (
            self.event_budget
                .high_count
                .load(std::sync::atomic::Ordering::Relaxed),
            self.event_budget
                .high_bytes
                .load(std::sync::atomic::Ordering::Relaxed),
        )
    }
    pub async fn next_event(&mut self) -> Option<AppServerEvent> {
        // Keep the current event charged until the caller asks for another one.
        self.in_flight.take();
        let event = match self.pending_events.pop_front() {
            Some(event) => event,
            None => self.event_rx.recv().await?,
        };
        let BufferedEvent {
            event,
            _count,
            _bytes,
        } = event;
        self.in_flight = Some((_count, _bytes));
        Some(event)
    }

    pub async fn shutdown(self) -> IoResult<()> {
        let Self {
            command_tx,
            event_rx,
            pending_events,
            event_budget: _,
            in_flight,
            metadata: _,
            mut worker_handle,
        } = self;
        drop(event_rx);
        drop(pending_events);
        drop(in_flight);
        let deadline = tokio::time::Instant::now() + SHUTDOWN_TIMEOUT;
        let close = tokio::time::timeout_at(deadline, async {
            let (response_tx, response_rx) = oneshot::channel();
            if command_tx
                .send(RemoteClientCommand::Shutdown { response_tx })
                .await
                .is_ok()
                && let Ok(result) = response_rx.await
            {
                result?;
            }
            Ok::<_, IoError>(())
        })
        .await;
        if tokio::time::timeout_at(deadline, &mut worker_handle)
            .await
            .is_err()
        {
            worker_handle.abort();
            let _ = worker_handle.await;
        }
        match close {
            Ok(result) => result,
            Err(_) => Err(IoError::new(
                ErrorKind::TimedOut,
                "remote shutdown total deadline exceeded; worker aborted",
            )),
        }
    }
}

impl RemoteAppServerRequestHandle {
    pub async fn request(&self, request: ClientRequest) -> IoResult<RequestResult> {
        self.request_json_rpc(jsonrpc_request_from_client_request(request))
            .await
    }

    pub async fn request_json_rpc(&self, request: JSONRPCRequest) -> IoResult<RequestResult> {
        let (response_tx, response_rx) = oneshot::channel();
        self.command_tx
            .send(RemoteClientCommand::Request {
                request: Box::new(request),
                response_tx,
            })
            .await
            .map_err(|_| {
                IoError::new(
                    ErrorKind::BrokenPipe,
                    "remote app-server worker channel is closed",
                )
            })?;
        response_rx.await.map_err(|_| {
            IoError::new(
                ErrorKind::BrokenPipe,
                "remote app-server request channel is closed",
            )
        })?
    }

    pub async fn request_typed<T>(&self, request: ClientRequest) -> Result<T, TypedRequestError>
    where
        T: DeserializeOwned,
    {
        let method = request.method_name();
        let response = self.request(request).await;
        decode_typed_response(method, response)
    }
}

async fn connect_websocket_endpoint(
    websocket_url: String,
    auth_token: Option<String>,
) -> IoResult<(String, WebSocketStream<MaybeTlsStream<TcpStream>>)> {
    let url = Url::parse(&websocket_url).map_err(|err| {
        IoError::new(
            ErrorKind::InvalidInput,
            format!("invalid websocket URL `{websocket_url}`: {err}"),
        )
    })?;
    if auth_token.is_some() && !websocket_url_supports_auth_token(&url) {
        return Err(IoError::new(
            ErrorKind::InvalidInput,
            format!(
                "remote auth tokens require `wss://` or loopback `ws://` URLs; got `{websocket_url}`"
            ),
        ));
    }

    let mut request = url.as_str().into_client_request().map_err(|err| {
        IoError::new(
            ErrorKind::InvalidInput,
            format!("invalid websocket URL `{websocket_url}`: {err}"),
        )
    })?;
    if let Some(auth_token) = auth_token.as_deref() {
        let header_value =
            HeaderValue::from_str(&format!("Bearer {auth_token}")).map_err(|err| {
                IoError::new(
                    ErrorKind::InvalidInput,
                    format!("invalid remote authorization header value: {err}"),
                )
            })?;
        request.headers_mut().insert(AUTHORIZATION, header_value);
    }

    ensure_rustls_crypto_provider();
    let websocket_config = remote_websocket_config();
    let stream = timeout(
        CONNECT_TIMEOUT,
        connect_async_with_config(
            request,
            Some(websocket_config),
            /*disable_nagle*/ false,
        ),
    )
    .await
    .map_err(|_| {
        IoError::new(
            ErrorKind::TimedOut,
            format!("timed out connecting to remote app server at `{websocket_url}`"),
        )
    })?
    .map(|(stream, _response)| stream)
    .map_err(|err| {
        IoError::other(format!(
            "failed to connect to remote app server at `{websocket_url}`: {err}"
        ))
    })?;

    Ok((websocket_url, stream))
}

async fn connect_unix_socket_endpoint(
    socket_path: AbsolutePathBuf,
    peer_policy: SocketPeerPolicy,
) -> IoResult<(String, WebSocketStream<UnixStream>)> {
    let endpoint = format!("unix://{}", socket_path.display());
    let request = UDS_WEBSOCKET_HANDSHAKE_URL
        .into_client_request()
        .map_err(|err| {
            IoError::new(
                ErrorKind::InvalidInput,
                format!("invalid UDS websocket handshake URL: {err}"),
            )
        })?;
    let stream = timeout(CONNECT_TIMEOUT, UnixStream::connect(socket_path.as_path()))
        .await
        .map_err(|_| {
            IoError::new(
                ErrorKind::TimedOut,
                format!("timed out connecting to remote app server at `{endpoint}`"),
            )
        })?
        .map_err(|err| {
            IoError::other(format!(
                "failed to connect to remote app server at `{endpoint}`: {err}"
            ))
        })?;
    match peer_policy {
        SocketPeerPolicy::ExplicitEndpoint => {}
        #[cfg(windows)]
        SocketPeerPolicy::NonElevatedCurrentUser => stream.ensure_non_elevated_peer()?,
    }
    let websocket_config = remote_websocket_config();
    let stream = timeout(
        CONNECT_TIMEOUT,
        client_async_with_config(request, stream, Some(websocket_config)),
    )
    .await
    .map_err(|_| {
        IoError::new(
            ErrorKind::TimedOut,
            format!("timed out upgrading remote app server at `{endpoint}`"),
        )
    })?
    .map(|(stream, _response)| stream)
    .map_err(|err| {
        IoError::other(format!(
            "failed to upgrade remote app server at `{endpoint}`: {err}"
        ))
    })?;

    Ok((endpoint, stream))
}

fn remote_websocket_config() -> WebSocketConfig {
    WebSocketConfig::default()
        .max_frame_size(Some(REMOTE_APP_SERVER_MAX_WEBSOCKET_MESSAGE_SIZE))
        .max_message_size(Some(REMOTE_APP_SERVER_MAX_WEBSOCKET_MESSAGE_SIZE))
}

async fn initialize_remote_connection<S>(
    stream: &mut WebSocketStream<S>,
    endpoint: &str,
    params: InitializeParams,
    initialize_timeout: Duration,
    budget: &EventBudget,
    max_response_bytes: usize,
) -> IoResult<(Vec<BufferedEvent>, RemoteServerMetadata)>
where
    S: AsyncRead + AsyncWrite + Unpin,
{
    let initialize_request_id = RequestId::String("initialize".to_string());
    let mut pending_events = Vec::new();
    let mut metadata = RemoteServerMetadata::default();
    write_jsonrpc_message(
        stream,
        JSONRPCMessage::Request(jsonrpc_request_from_client_request(
            ClientRequest::Initialize {
                request_id: initialize_request_id.clone(),
                params,
            },
        )),
        endpoint,
    )
    .await?;

    timeout(initialize_timeout, async {
        loop {
            match stream.next().await {
                Some(Ok(Message::Text(text))) => {
                    let message = serde_json::from_str::<JSONRPCMessage>(&text).map_err(|err| {
                        IoError::other(format!(
                            "remote app server at `{endpoint}` sent invalid initialize response: {err}"
                        ))
                    })?;
                    match message {
                        JSONRPCMessage::Response(response) if response.id == initialize_request_id => {
                            if encoded_len(&response.result)?>max_response_bytes {return Err(IoError::new(ErrorKind::InvalidData,"initialize result exceeds RPC byte limit"));}
                            metadata.server_version = response
                                .result
                                .get("userAgent")
                                .and_then(serde_json::Value::as_str)
                                .and_then(|user_agent| {
                                    let (_, rest) = user_agent.split_once('/')?;
                                    rest.split_whitespace().next().map(str::to_string)
                                });
                            metadata.codex_home = response
                                .result
                                .get("codexHome")
                                .and_then(serde_json::Value::as_str)
                                .filter(|codex_home| !codex_home.is_empty())
                                .map(str::to_string);
                            metadata.platform_family = response.result.get("platformFamily")
                                .and_then(serde_json::Value::as_str).map(str::to_string);
                            metadata.platform_os = response.result.get("platformOs")
                                .and_then(serde_json::Value::as_str).map(str::to_string);
                            break Ok(());
                        }
                        JSONRPCMessage::Error(error) if error.id == initialize_request_id => {
                            break Err(IoError::other(format!(
                                "remote app server at `{endpoint}` rejected initialize: {}",
                                error.error.message
                            )));
                        }
                        JSONRPCMessage::Notification(notification) => {
                            if let Some(event) = app_server_event_from_notification(notification) {
                                pending_events.push(budget.initial(event)?);
                            }
                        }
                        JSONRPCMessage::Request(request) => {
                            let request_id = request.id.clone();
                            let method = request.method.clone();
                            match ServerRequest::try_from(request) {
                                Ok(request) => {
                                    pending_events
                                        .push(budget.initial(AppServerEvent::ServerRequest(Box::new(request)))?);
                                }
                                Err(err) => {
                                    warn!(%err, method, "rejecting unknown remote app-server request during initialize");
                                    write_jsonrpc_message(
                                        stream,
                                        JSONRPCMessage::Error(JSONRPCError {
                                            error: JSONRPCErrorError {
                                                code: -32601,
                                                message: format!(
                                                    "unsupported remote app-server request `{method}`"
                                                ),
                                                data: None,
                                            },
                                            id: request_id,
                                        }),
                                        endpoint,
                                    )
                                    .await?;
                                }
                            }
                        }
                        JSONRPCMessage::Response(_) | JSONRPCMessage::Error(_) => {}
                    }
                }
                Some(Ok(Message::Binary(_)))=>break Err(IoError::new(ErrorKind::InvalidData,"unsupported binary initialize frame")),
                Some(Ok(Message::Ping(_)))
                | Some(Ok(Message::Pong(_)))
                | Some(Ok(Message::Frame(_))) => {}
                Some(Ok(Message::Close(frame))) => {
                    let reason = frame
                        .as_ref()
                        .map(|frame| frame.reason.to_string())
                        .filter(|reason| !reason.is_empty())
                        .unwrap_or_else(|| "connection closed during initialize".to_string());
                    break Err(IoError::new(
                        ErrorKind::ConnectionAborted,
                        format!(
                            "remote app server at `{endpoint}` closed during initialize: {reason}"
                        ),
                    ));
                }
                Some(Err(err)) => {
                    break Err(IoError::other(format!(
                        "remote app server at `{endpoint}` transport failed during initialize: {err}"
                    )));
                }
                None => {
                    break Err(IoError::new(
                        ErrorKind::UnexpectedEof,
                        format!("remote app server at `{endpoint}` closed during initialize"),
                    ));
                }
            }
        }
    })
    .await
    .map_err(|_| {
        IoError::new(
            ErrorKind::TimedOut,
            format!("timed out waiting for initialize response from `{endpoint}`"),
        )
    })??;

    write_jsonrpc_message(
        stream,
        JSONRPCMessage::Notification(jsonrpc_notification_from_client_notification(
            ClientNotification::Initialized,
        )),
        endpoint,
    )
    .await?;

    Ok((pending_events, metadata))
}

fn app_server_event_from_notification(notification: JSONRPCNotification) -> Option<AppServerEvent> {
    let method = notification.method.clone();
    if !KNOWN_NOTIFICATION_METHODS.contains(&method.as_str()) {
        return Some(AppServerEvent::UnsupportedNotification { method });
    }
    let thread_id = if THREAD_NOTIFICATION_METHODS.contains(&method.as_str()) {
        notification
            .params
            .as_ref()
            .and_then(|params| params.get("threadId"))
            .and_then(serde_json::Value::as_str)
            .filter(|id| !id.is_empty() && id.len() <= 4096)
            .map(str::to_owned)
    } else {
        None
    };
    match ServerNotification::try_from(notification) {
        Ok(notification) => Some(AppServerEvent::ServerNotification(Box::new(notification))),
        Err(error) => Some(AppServerEvent::NotificationDecodeError {
            message: format!(
                "invalid known remote notification {method}: {error}; history incomplete"
            ),
            method,
            thread_id,
        }),
    }
}

async fn deliver_event(event_tx: &EventSender, event: AppServerEvent) -> IoResult<()> {
    let (event, bytes) = match event_tx.budget.size(&event) {
        Ok(bytes) => (event, bytes),
        Err(error) => {
            let event = AppServerEvent::Disconnected {
                message: error.to_string(),
            };
            let bytes = event_tx.budget.size(&event)?;
            (event, bytes)
        }
    };
    let fatal = matches!(event, AppServerEvent::Disconnected { .. });
    let count = event_tx
        .budget
        .count
        .clone()
        .acquire_owned()
        .await
        .map_err(IoError::other)?;
    let bytes = event_tx
        .budget
        .bytes
        .clone()
        .acquire_many_owned(bytes as u32)
        .await
        .map_err(IoError::other)?;
    event_tx.budget.record_high_water();
    event_tx
        .sender
        .send(BufferedEvent {
            event,
            _count: count,
            _bytes: bytes,
        })
        .await
        .map_err(|_| {
            IoError::new(
                ErrorKind::BrokenPipe,
                "remote event consumer channel is closed",
            )
        })?;
    if fatal {
        return Err(IoError::new(
            ErrorKind::InvalidData,
            "remote event stream failed explicitly",
        ));
    }
    Ok(())
}

fn jsonrpc_request_from_client_request(request: ClientRequest) -> JSONRPCRequest {
    let value = match serde_json::to_value(request) {
        Ok(value) => value,
        Err(err) => panic!("client request should serialize: {err}"),
    };
    match serde_json::from_value(value) {
        Ok(request) => request,
        Err(err) => panic!("client request should encode as JSON-RPC request: {err}"),
    }
}

fn jsonrpc_notification_from_client_notification(
    notification: ClientNotification,
) -> JSONRPCNotification {
    let value = match serde_json::to_value(notification) {
        Ok(value) => value,
        Err(err) => panic!("client notification should serialize: {err}"),
    };
    match serde_json::from_value(value) {
        Ok(notification) => notification,
        Err(err) => panic!("client notification should encode as JSON-RPC notification: {err}"),
    }
}

async fn write_jsonrpc_message<S>(
    stream: &mut WebSocketStream<S>,
    message: JSONRPCMessage,
    endpoint: &str,
) -> IoResult<()>
where
    S: AsyncRead + AsyncWrite + Unpin,
{
    let payload = serde_json::to_string(&message).map_err(IoError::other)?;
    stream
        .send(Message::Text(payload.into()))
        .await
        .map_err(|err| {
            IoError::other(format!(
                "failed to write websocket message to `{endpoint}`: {err}"
            ))
        })
}

fn websocket_close_error_is_already_closed(err: &TungsteniteError) -> bool {
    match err {
        TungsteniteError::ConnectionClosed | TungsteniteError::AlreadyClosed => true,
        TungsteniteError::Io(err) => matches!(
            err.kind(),
            ErrorKind::BrokenPipe | ErrorKind::ConnectionReset | ErrorKind::NotConnected
        ),
        _ => false,
    }
}
#[cfg(test)]
#[allow(clippy::expect_used)] // Match the official test assertion style.
mod tests {
    use super::*;

    #[tokio::test]
    async fn shutdown_tolerates_worker_exit_after_command_is_queued() {
        let (command_tx, mut command_rx) = mpsc::channel(1);
        let (_event_tx, event_rx) = mpsc::channel::<BufferedEvent>(1);
        let worker_handle = tokio::spawn(async move {
            let _ = command_rx.recv().await;
        });
        let client = RemoteAppServerClient {
            command_tx,
            event_rx,
            pending_events: VecDeque::new(),
            event_budget: EventBudget::new(1, REMOTE_EVENT_BYTES),
            in_flight: None,
            metadata: RemoteServerMetadata::default(),
            worker_handle: AbortOnDrop(worker_handle),
        };

        client
            .shutdown()
            .await
            .expect("shutdown should complete when worker exits first");
    }
    #[tokio::test]
    async fn cancelled_shutdown_aborts_owned_official_worker() {
        struct Done(Option<oneshot::Sender<()>>);
        impl Drop for Done {
            fn drop(&mut self) {
                if let Some(tx) = self.0.take() {
                    let _ = tx.send(());
                }
            }
        }
        let (done_tx, done_rx) = oneshot::channel();
        let (command_tx, _command_rx) = mpsc::channel(1);
        let (reply, _) = oneshot::channel();
        command_tx
            .try_send(RemoteClientCommand::Notify {
                notification: ClientNotification::Initialized,
                response_tx: reply,
            })
            .expect("fill command channel");
        let (_sender, event_rx) = mpsc::channel(1);
        let worker = tokio::spawn(async move {
            let _done = Done(Some(done_tx));
            std::future::pending::<()>().await;
        });
        tokio::task::yield_now().await;
        let client = RemoteAppServerClient {
            command_tx,
            event_rx,
            pending_events: VecDeque::new(),
            event_budget: EventBudget::new(1, REMOTE_EVENT_BYTES),
            in_flight: None,
            metadata: RemoteServerMetadata::default(),
            worker_handle: AbortOnDrop(worker),
        };
        assert!(
            timeout(Duration::from_millis(30), client.shutdown())
                .await
                .is_err()
        );
        timeout(Duration::from_secs(1), done_rx)
            .await
            .expect("aborted worker released")
            .expect("worker guard dropped");
    }
}
