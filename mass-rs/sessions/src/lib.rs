mod rpc;
pub use rpc::RpcError;
use rpc::unsupported;
mod registration;
use mass_domain::{SessionMetadata, SessionSpec};
use mass_events::{
    Events,
    handlers::{
        DeliveryKind, HandlerContext, HandlerRegistration, HandlerTasks, ImmutableEvent,
        SessionIdentity,
    },
};
use mass_journal as storage;
use mass_runtime::{AttachmentControl, OpenRequest, Runtime, SessionCommands};
pub use registration::Registration;
use serde::Deserialize;
use serde_json::{Value, json};
use std::{
    collections::{BTreeMap, BTreeSet},
    path::PathBuf,
    sync::{
        Arc, Mutex as StdMutex,
        atomic::{AtomicBool, Ordering},
    },
    time::Duration,
};
use tokio::sync::{Mutex, RwLock, watch};

struct Attachment {
    handle: Arc<dyn AttachmentControl>,
    events: Arc<Mutex<Events>>,
}
#[derive(Clone)]
pub struct Service {
    data_dir: PathBuf,
    registry: Arc<registration::Registry>,
    providers: BTreeMap<String, Arc<dyn Runtime>>,
    runtime_types: Vec<String>,
    handlers: Arc<HandlerTasks>,
    shutting_down: Arc<AtomicBool>,
    stopped: watch::Sender<bool>,
    sessions: Arc<Mutex<BTreeMap<String, SessionMetadata>>>,
    reservations: Arc<StdMutex<BTreeSet<String>>>,
    creations: Arc<RwLock<()>>,
    attachments: Arc<StdMutex<BTreeMap<String, Arc<Attachment>>>>,
    lifecycles: Arc<StdMutex<BTreeMap<String, Arc<Mutex<()>>>>>,
}
fn text_capabilities(connected: bool) -> Value {
    json!({"prompt":connected,"inputTypes":["text"],"concurrentInput":false,"steer":false,"cancel":connected,"archive":true,"resume":!connected,"setConfigOption":false,"getEvent":true,"watchEvent":connected,"tools":if connected { json!(["shell"]) } else { json!([]) },"toolOutput":"retained_or_unavailable","toolApprovals":connected,"interactiveRequests":false,"thinking":"public_only","backgroundLifecycle":false})
}
#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct NewSession {
    session_name: String,
    spec: SessionSpec,
}
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Selector {
    session_name: String,
}
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct HistoryQuery {
    session_name: String,
    #[serde(default)]
    after_seq: u64,
    #[serde(default = "default_limit")]
    limit: usize,
}
fn default_limit() -> usize {
    100
}
fn parse<T: serde::de::DeserializeOwned>(params: Value) -> Result<T, RpcError> {
    serde_json::from_value(params).map_err(|e| RpcError::InvalidParams(e.to_string()))
}

struct NameReservation {
    names: Arc<StdMutex<BTreeSet<String>>>,
    name: String,
    cleanup: StdMutex<Option<PathBuf>>,
    operation_id: String,
    instance: Value,
}
impl Drop for NameReservation {
    fn drop(&mut self) {
        if let Some(directory) = self.cleanup.lock().unwrap().take() {
            let _ = std::fs::remove_dir_all(directory);
        }
        tracing::debug!(operation_id = %self.operation_id, instance = %self.instance["identity"], "session open ownership settled");
        self.names.lock().unwrap().remove(&self.name);
    }
}
impl Service {
    /// In-process composition uses the same durable registration path as the daemon.
    pub fn new<T: Runtime + 'static>(
        data_dir: PathBuf,
        providers: BTreeMap<String, Arc<T>>,
        handlers: Vec<HandlerRegistration>,
        shutdown_timeout: Duration,
    ) -> anyhow::Result<Self> {
        let registrations = providers
            .iter()
            .map(|(name, runtime)| {
                Ok(Registration {
                    name: name.clone(),
                    runtime_type: runtime.runtime_type().into(),
                    fingerprint: runtime.fingerprint()?,
                    enabled: true,
                })
            })
            .collect::<anyhow::Result<Vec<_>>>()?;
        Self::registered(
            data_dir,
            providers,
            registrations,
            handlers,
            shutdown_timeout,
        )
    }
    pub fn registered<T: Runtime + 'static>(
        data_dir: PathBuf,
        providers: BTreeMap<String, Arc<T>>,
        registrations: Vec<Registration>,
        handlers: Vec<HandlerRegistration>,
        shutdown_timeout: Duration,
    ) -> anyhow::Result<Self> {
        Self::with_runtimes(
            data_dir,
            providers
                .into_iter()
                .map(|(name, runtime)| (name, runtime as Arc<dyn Runtime>))
                .collect(),
            registrations,
            handlers,
            shutdown_timeout,
        )
    }
    /// The composition root supplies all available types, including unconfigured ones.
    pub fn with_runtime_types(mut self, types: Vec<String>) -> Self {
        self.runtime_types = types
            .into_iter()
            .collect::<BTreeSet<_>>()
            .into_iter()
            .collect();
        self
    }
    /// Runtime-only seam; Session orchestration cannot reach a concrete adapter or Host.
    pub fn with_runtimes(
        data_dir: PathBuf,
        providers: BTreeMap<String, Arc<dyn Runtime>>,
        registrations: Vec<Registration>,
        handlers: Vec<HandlerRegistration>,
        shutdown_timeout: Duration,
    ) -> anyhow::Result<Self> {
        let data_dir = std::path::absolute(data_dir)?;
        let registry = registration::Registry::load(&data_dir, registrations)?;
        let sessions = storage::load(&data_dir)?;
        let runtime_types = providers
            .values()
            .map(|runtime| runtime.runtime_type().to_owned())
            .collect::<BTreeSet<_>>()
            .into_iter()
            .collect();
        Ok(Self {
            runtime_types,
            data_dir,
            registry: Arc::new(registry),
            providers,
            handlers: Arc::new(HandlerTasks::new(handlers, shutdown_timeout)),
            shutting_down: Arc::new(AtomicBool::new(false)),
            stopped: watch::channel(false).0,
            sessions: Arc::new(Mutex::new(sessions)),
            reservations: Arc::new(StdMutex::new(BTreeSet::new())),
            creations: Arc::new(RwLock::new(())),
            attachments: Arc::new(StdMutex::new(BTreeMap::new())),
            lifecycles: Arc::new(StdMutex::new(BTreeMap::new())),
        })
    }
    fn ensure_enabled(&self, name: &str) -> Result<(), RpcError> {
        if !self.registry.enabled(name) {
            return Err(RpcError::InvalidParams(
                "runtime is disabled or omitted".into(),
            ));
        }
        Ok(())
    }
    async fn runtime_view(&self, name: &str) -> Result<Value, RpcError> {
        let provider = self
            .providers
            .get(name)
            .ok_or_else(|| RpcError::InvalidParams("unknown runtime name".into()))?;
        let enabled = self.registry.enabled(name);
        Ok(
            json!({"name":name,"type":provider.runtime_type(),"enabled":enabled,"status":provider.status().await}),
        )
    }
    pub async fn ready(&self) -> bool {
        if self.shutting_down.load(Ordering::Acquire) {
            return false;
        }
        if self.providers.is_empty() {
            return true;
        }
        for name in self.providers.keys() {
            let view = self.runtime_view(name).await.expect("configured runtime");
            if view["enabled"] == true
                && matches!(view["status"]["phase"].as_str(), Some("pending" | "ready"))
            {
                return true;
            }
        }
        false
    }
    pub async fn availability(&self) -> (usize, usize) {
        (
            self.providers.len(),
            self.attachments
                .lock()
                .unwrap()
                .values()
                .filter(|r| r.handle.online() && !r.handle.closing())
                .count(),
        )
    }
    async fn with_live_state(&self, metadata: &SessionMetadata) -> Value {
        let mut value = serde_json::to_value(metadata).expect("serializable metadata");
        let handle = self
            .attachments
            .lock()
            .unwrap()
            .get(&metadata.session_name)
            .cloned();
        let closing = handle.as_ref().is_some_and(|a| a.handle.closing());
        let connected = handle.as_ref().is_some_and(|a| a.handle.online()) && !closing;
        value["connected"] = json!(connected);
        value["lifecycle"] = json!(if closing {
            "closing"
        } else if connected {
            "online"
        } else {
            "offline"
        });
        for key in [
            "state",
            "sessionInfo",
            "configOptions",
            "availableCommands",
            "usage",
        ] {
            value[key] = Value::Null;
        }
        if let Some(handle) = handle.filter(|_| connected) {
            let events = handle.events.lock().await;
            for key in [
                "state",
                "sessionInfo",
                "configOptions",
                "availableCommands",
                "usage",
            ] {
                value[key] = events.snapshot()[key].clone();
            }
        }
        value.as_object_mut().unwrap().remove("instance");
        value.as_object_mut().unwrap().remove("recovery");
        value["capabilities"] = text_capabilities(value["connected"] == true);
        if closing
            || self
                .providers
                .get(&metadata.spec.runtime_name)
                .is_none_or(|r| !r.supports_recovery())
        {
            value["capabilities"]["resume"] = json!(false);
        }
        value
    }
    fn lifecycle(&self, name: &str) -> Arc<Mutex<()>> {
        self.lifecycles
            .lock()
            .unwrap()
            .entry(name.to_owned())
            .or_default()
            .clone()
    }
    async fn archive(&self, selector: Selector) -> Result<Value, RpcError> {
        let operation_id = uuid::Uuid::now_v7().to_string();
        let service = self.clone();
        let mut value = tokio::spawn(async move { service.archive_owned(selector).await })
            .await
            .map_err(|_| RpcError::Internal(anyhow::anyhow!("session operation failed")))?
            .map_err(|error| operation_error(error, &operation_id))?;
        value["operationId"] = json!(operation_id);
        Ok(value)
    }
    async fn archive_owned(&self, selector: Selector) -> Result<Value, RpcError> {
        self.ensure_running()?;
        let lifecycle = self.lifecycle(&selector.session_name);
        let _guard = lifecycle.lock().await;
        let metadata = self
            .sessions
            .lock()
            .await
            .get(&selector.session_name)
            .cloned()
            .ok_or_else(|| RpcError::InvalidParams("unknown session".into()))?;
        let attachment = self
            .attachments
            .lock()
            .unwrap()
            .get(&selector.session_name)
            .cloned();
        if let Some(attachment) = attachment
            && (attachment.handle.online() || attachment.handle.closing())
        {
            attachment.handle.clone().close().await.map_err(|_error| {
                RpcError::Structured(rpc::error(
                    -32603,
                    "close_unknown",
                    "Session release was not confirmed",
                    json!({}),
                ))
            })?;
        }
        Ok(self.with_live_state(&metadata).await)
    }
    async fn resume(&self, selector: Selector) -> Result<Value, RpcError> {
        let operation_id = uuid::Uuid::now_v7().to_string();
        let service = self.clone();
        let operation = operation_id.clone();
        let mut value =
            tokio::spawn(async move { service.resume_owned(selector, &operation).await })
                .await
                .map_err(|_| RpcError::Internal(anyhow::anyhow!("session operation failed")))?
                .map_err(|error| operation_error(error, &operation_id))?;
        value["operationId"] = json!(operation_id);
        Ok(value)
    }
    async fn resume_owned(
        &self,
        selector: Selector,
        operation_id: &str,
    ) -> Result<Value, RpcError> {
        let _creation = self.creations.read().await;
        self.ensure_running()?;
        let lifecycle = self.lifecycle(&selector.session_name);
        let _guard = lifecycle.lock().await;
        let metadata = self
            .sessions
            .lock()
            .await
            .get(&selector.session_name)
            .cloned()
            .ok_or_else(|| RpcError::InvalidParams("unknown session".into()))?;
        if self
            .providers
            .get(&metadata.spec.runtime_name)
            .is_some_and(|r| !r.supports_recovery())
        {
            return Err(anyhow::Error::new(mass_runtime::LifecycleError {
                kind: "unsupported_capability",
                message: "This Runtime cannot verify recovery ownership",
            })
            .into());
        }
        if let Some(attachment) = self
            .attachments
            .lock()
            .unwrap()
            .get(&selector.session_name)
            .cloned()
        {
            if attachment.handle.closing() {
                return Err(RpcError::Structured(rpc::error(
                    -32603,
                    "close_unknown",
                    "previous attachment is still closing",
                    json!({}),
                )));
            }
            if attachment.handle.online() {
                return Err(RpcError::InvalidParams("session is already online".into()));
            }
        }
        if !self
            .reservations
            .lock()
            .unwrap()
            .insert(metadata.session_name.clone())
        {
            return Err(RpcError::InvalidParams(
                "session open is still settling".into(),
            ));
        }
        let reservation = Arc::new(NameReservation {
            names: self.reservations.clone(),
            name: metadata.session_name.clone(),
            cleanup: StdMutex::new(None),
            operation_id: operation_id.to_owned(),
            instance: metadata.instance.clone(),
        });
        // Re-read the original metadata. Never repair or spawn from stale/changed binding.
        storage::validate_metadata(&self.data_dir, &metadata)?;
        let provider = self
            .providers
            .get(&metadata.spec.runtime_name)
            .ok_or_else(|| RpcError::InvalidParams("unknown runtimeName".into()))?;
        self.ensure_enabled(&metadata.spec.runtime_name)?;
        if metadata.instance != self.registry.get_binding(&metadata.spec.runtime_name) {
            return Err(RpcError::InvalidParams("runtime identity mismatch".into()));
        }
        let path = storage::directory(&self.data_dir, &metadata).join("events.jsonl");
        let events = Arc::new(Mutex::new(Events::resume(
            metadata.session_name.clone(),
            metadata.session_id.clone(),
            path,
            || {
                provider.validate_cwd(&metadata.spec.cwd)?;
                provider.validate_recovery(
                    &metadata.session_id,
                    &metadata.recovery,
                    &metadata.spec.cwd,
                )
            },
            provider.validate_history_record(),
        )?));
        let connection_id = uuid::Uuid::now_v7();
        let sink = events.clone();
        let opened = provider
            .clone()
            .open(OpenRequest {
                cwd: metadata.spec.cwd.clone(),
                recovery: Some((metadata.session_id.clone(), metadata.recovery.clone())),
                events: Arc::new(move |_| sink.clone()),
                connection_id: connection_id.to_string(),
                deadline: tokio::time::Instant::now() + Duration::from_secs(30),
                owner: reservation,
            })
            .await?;
        let handle = opened.control;
        if opened.session_id != metadata.session_id {
            let _ = handle.clone().close().await;
            return Err(anyhow::anyhow!("resume identity mismatch").into());
        }
        events.lock().await.activate(
            &self.handlers,
            HandlerContext {
                session: SessionIdentity {
                    session_name: metadata.session_name.clone(),
                    session_id: metadata.session_id.clone(),
                    runtime_instance_id: metadata.instance["identity"].as_str().unwrap().to_owned(),
                    connection_id,
                },
                delivery: DeliveryKind::Transient,
                commands: SessionCommands::new(handle.clone()),
            },
        );
        handle.commit();
        self.attachments.lock().unwrap().insert(
            metadata.session_name.clone(),
            Arc::new(Attachment { handle, events }),
        );
        Ok(self.with_live_state(&metadata).await)
    }
    async fn create(&self, params: NewSession) -> Result<Value, RpcError> {
        // The task owns the accepted operation even when its HTTP observer disconnects.
        let operation_id = uuid::Uuid::now_v7().to_string();
        let service = self.clone();
        let operation = operation_id.clone();
        let task = tokio::spawn(async move { service.create_owned(params, &operation).await });
        task.await
            .map_err(|_| RpcError::Internal(anyhow::anyhow!("session operation failed")))?
            .map_err(|error| operation_error(error, &operation_id))
    }
    async fn create_owned(
        &self,
        params: NewSession,
        operation_id: &str,
    ) -> Result<Value, RpcError> {
        let _creation = self.creations.read().await;
        let NewSession { session_name, spec } = params;
        self.ensure_running()?;
        if session_name.trim().is_empty() {
            return Err(RpcError::InvalidParams(
                "sessionName must be nonempty".into(),
            ));
        }
        let provider = self
            .providers
            .get(&spec.runtime_name)
            .ok_or_else(|| RpcError::InvalidParams("unknown runtimeName".into()))?
            .clone();
        self.ensure_enabled(&spec.runtime_name)?;
        provider
            .validate_cwd(&spec.cwd)
            .map_err(|e| RpcError::InvalidParams(e.to_string()))?;
        {
            let sessions = self.sessions.lock().await;
            if sessions.contains_key(&session_name)
                || !self
                    .reservations
                    .lock()
                    .unwrap()
                    .insert(session_name.clone())
            {
                return Err(RpcError::InvalidParams("sessionName already exists".into()));
            }
        }
        let reservation = Arc::new(NameReservation {
            names: self.reservations.clone(),
            name: session_name.clone(),
            cleanup: StdMutex::new(None),
            operation_id: operation_id.to_owned(),
            instance: self.registry.get_binding(&spec.runtime_name),
        });
        let lifecycle = self.lifecycle(&session_name);
        let _guard = lifecycle.lock().await;
        let instance = self.registry.get_binding(&spec.runtime_name);
        let connection_id = uuid::Uuid::now_v7();
        let event_slot = Arc::new(StdMutex::new(None));
        let slot = event_slot.clone();
        let root = self.data_dir.clone();
        let binding = instance.clone();
        let name = session_name.clone();
        let opened = provider
            .open(OpenRequest {
                cwd: spec.cwd.clone(),
                recovery: None,
                connection_id: connection_id.to_string(),
                deadline: tokio::time::Instant::now() + Duration::from_secs(30),
                owner: reservation.clone(),
                events: Arc::new(move |id| {
                    let events = Arc::new(Mutex::new(Events::new(
                        name.clone(),
                        id.into(),
                        storage::session_directory(&root, &binding, id).join("events.jsonl"),
                    )));
                    *slot.lock().unwrap() = Some(events.clone());
                    events
                }),
            })
            .await?;
        let handle = opened.control;
        let session_id = opened.session_id;
        let metadata = SessionMetadata {
            session_name: session_name.clone(),
            session_id: session_id.clone(),
            format_version: 2,
            instance,
            recovery: opened.recovery,
            history_mode: "paginated".into(),
            spec,
            config_options: json!([]),
            capabilities: text_capabilities(true),
        };
        let directory = storage::directory(&self.data_dir, &metadata);
        let saved = (|| -> anyhow::Result<()> {
            // Exclusive creation prevents duplicate Agent IDs from replacing a committed pair.
            std::fs::create_dir(&directory)?;
            *reservation.cleanup.lock().unwrap() = Some(directory.clone());
            storage::save(&self.data_dir, &metadata)
        })();
        if let Err(error) = saved {
            tracing::warn!(%error, %operation_id, "session metadata save failed");
            let release = handle.clone().close().await;
            return Err(RpcError::Structured(rpc::error(
                -32603,
                if release.is_ok() {
                    "persistence_failed"
                } else {
                    "close_unknown"
                },
                "Session metadata could not be saved",
                json!({}),
            )));
        }
        let events = event_slot
            .lock()
            .unwrap()
            .take()
            .expect("opened attachment bound its event sink");
        let pending_handlers = self
            .handlers
            .pending(metadata.instance["identity"].as_str().unwrap(), &session_id);
        *reservation.cleanup.lock().unwrap() = None;
        handle.commit();
        events.lock().await.activate(
            &self.handlers,
            HandlerContext {
                session: SessionIdentity {
                    session_name,
                    session_id,
                    runtime_instance_id: metadata.instance["identity"].as_str().unwrap().to_owned(),
                    connection_id,
                },
                delivery: DeliveryKind::Transient,
                commands: SessionCommands::new(handle.clone()),
            },
        );
        {
            let mut sessions = self.sessions.lock().await;
            sessions.insert(metadata.session_name.clone(), metadata.clone());
            self.attachments.lock().unwrap().insert(
                metadata.session_name.clone(),
                Arc::new(Attachment {
                    handle: handle.clone(),
                    events: events.clone(),
                }),
            );
        }
        pending_handlers.accept();
        let mut response = self.with_live_state(&metadata).await;
        response["operationId"] = json!(operation_id);
        Ok(response)
    }
    fn ensure_running(&self) -> Result<(), RpcError> {
        if self.shutting_down.load(Ordering::Acquire) {
            Err(RpcError::Internal(anyhow::anyhow!(
                "service is shutting down"
            )))
        } else {
            Ok(())
        }
    }
    /// Transport consumers close after accepted core writes and Handler shutdown.
    pub fn stopped(&self) -> watch::Receiver<bool> {
        self.stopped.subscribe()
    }
    pub async fn shutdown(&self) {
        self.shutting_down.store(true, Ordering::Release);
        for provider in self.providers.values() {
            provider.close_admission().await;
        }
        let _creations = self.creations.write().await;
        let mut released = true;
        for provider in self.providers.values() {
            if provider.stop().await.is_err() {
                released = false;
            }
        }
        self.handlers.shutdown().await;
        if released {
            self.registry.release_lock();
        }
        self.stopped.send_replace(true);
    }
    async fn execute(&self, method: &str, params: Value) -> Result<Value, RpcError> {
        self.ensure_running()?;
        if method != "agentd/session/prompt" && method != "agentd/session/cancel" {
            return Err(unsupported());
        }
        let selector: Selector = parse(params.clone())?;
        let handle = self
            .attachments
            .lock()
            .unwrap()
            .get(&selector.session_name)
            .cloned()
            .ok_or_else(|| RpcError::InvalidParams("session is unknown or offline".into()))?;
        let commands = SessionCommands::new(handle.handle.clone());
        if method == "agentd/session/prompt" {
            let blocks = params["prompt"]
                .as_array()
                .ok_or_else(|| RpcError::InvalidParams("prompt must be an array".into()))?;
            Ok(serde_json::to_value(
                commands
                    .prompt(blocks.clone())
                    .await
                    .map_err(RpcError::from)?,
            )
            .map_err(anyhow::Error::from)?)
        } else {
            commands.cancel().await.map_err(RpcError::from)?;
            Ok(json!({}))
        }
    }
    pub async fn watch(
        &self,
        params: Value,
    ) -> Result<tokio::sync::broadcast::Receiver<ImmutableEvent>, RpcError> {
        let selector: Selector = parse(params.clone())?;
        for key in ["afterSeq", "lastSeq", "replayFrom"] {
            if params.get(key).is_some() {
                return Err(RpcError::InvalidParams("watch_event is live-only".into()));
            }
        }
        let events = self
            .attachments
            .lock()
            .unwrap()
            .get(&selector.session_name)
            .filter(|r| r.handle.online() && !r.handle.closing())
            .map(|r| r.events.clone())
            .ok_or_else(|| RpcError::InvalidParams("session is unknown or offline".into()))?;
        let receiver = events.lock().await.subscribe();
        Ok(receiver)
    }

    pub async fn call(&self, method: &str, params: Value) -> Result<Value, RpcError> {
        match method {
            "agentd/runtime/types" => Ok(json!(
                self.runtime_types
                    .iter()
                    .map(|kind| json!({"type":kind}))
                    .collect::<Vec<_>>()
            )),
            "agentd/runtime/list" => {
                let mut result = Vec::new();
                for name in self.providers.keys() {
                    result.push(self.runtime_view(name).await?);
                }
                Ok(json!(result))
            }
            "agentd/runtime/status" => {
                #[derive(Deserialize)]
                #[serde(deny_unknown_fields)]
                struct RuntimeSelector {
                    name: String,
                }
                let selector: RuntimeSelector = parse(params)?;
                self.runtime_view(&selector.name).await
            }
            "agentd/status" => {
                let (configured, connected) = self.availability().await;
                Ok(
                    json!({"status":if self.ready().await {"ready"} else {"unavailable"},"agents":self.providers.keys().collect::<Vec<_>>(),"configured":configured,"connected":connected}),
                )
            }
            "agentd/session/list" => {
                let sessions = self.sessions.lock().await.clone();
                let mut result = Vec::new();
                for metadata in sessions.values() {
                    result.push(self.with_live_state(metadata).await);
                }
                Ok(json!(result))
            }
            "agentd/session/status" => {
                let selector: Selector = parse(params)?;
                let sessions = self.sessions.lock().await.clone();
                let metadata = sessions
                    .get(&selector.session_name)
                    .ok_or_else(|| RpcError::InvalidParams("unknown session".into()))?;
                Ok(self.with_live_state(metadata).await)
            }
            "agentd/session/archive" => self.archive(parse(params)?).await,
            "agentd/session/resume" => self.resume(parse(params)?).await,
            "agentd/session/new" => self.create(parse(params)?).await,
            "agentd/session/prompt"
            | "agentd/session/cancel"
            | "agentd/session/set_config_option" => self.execute(method, params).await,
            "agentd/session/get_event" => {
                let query: HistoryQuery = parse(params)?;
                if query.limit == 0 || query.limit > 1000 {
                    return Err(RpcError::InvalidParams("limit must be 1..1000".into()));
                }
                let sessions = self.sessions.lock().await;
                let metadata = sessions
                    .get(&query.session_name)
                    .ok_or_else(|| RpcError::InvalidParams("unknown session".into()))?;
                let events = storage::history_for(
                    &storage::directory(&self.data_dir, metadata).join("events.jsonl"),
                    &metadata.session_name,
                    &metadata.session_id,
                    query.after_seq,
                    query.limit,
                )?;
                Ok(
                    json!({"events":events,"nextSeq":events.last().map(|e| e["seq"].clone()).unwrap_or(json!(query.after_seq))}),
                )
            }
            _ => Err(RpcError::MethodNotFound),
        }
    }
}

fn operation_error(error: RpcError, operation_id: &str) -> RpcError {
    let mut value = match error {
        RpcError::Internal(error) if error.is::<mass_runtime::OpenUnknown>() => {
            rpc::error(-32603, "open_unknown", &error.to_string(), json!({}))
        }
        RpcError::Internal(error) if error.is::<mass_runtime::LifecycleError>() => {
            let failure = error
                .downcast_ref::<mass_runtime::LifecycleError>()
                .unwrap();
            rpc::error(-32603, failure.kind, failure.message, json!({}))
        }
        error => error.response(),
    };
    value["data"]["operationId"] = json!(operation_id);
    RpcError::Structured(value)
}
