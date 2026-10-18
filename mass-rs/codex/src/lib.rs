//! Fixed Codex 0.160.1 native connector. Runtime owns transport; attachments own admission.
mod delivery;
mod recovery;
mod remote;
use delivery::FactLane;
use remote::LocalResources;
pub use remote::RemoteRegistrationConfig;

use anyhow::{Context, Result, bail, ensure};
use codex_app_server_client::{
    AppServerClient, AppServerEvent, RemoteAppServerClient, RemoteAppServerConnectArgs,
    RemoteAppServerEndpoint,
};
use codex_app_server_protocol::{
    ClientRequest, CommandExecutionRequestApprovalResponse, JSONRPCErrorError, RequestId,
    ThreadResumeParams, ThreadStartParams, ThreadUnsubscribeParams, TurnInterruptParams,
    TurnStartParams,
};
use futures_util::{FutureExt, StreamExt, stream::FuturesUnordered};
use mass_runtime::{
    Acceptance, AcceptanceEvidence, CommandError, CommandErrorKind, InputBlock, InputCommand,
    InputReceipt, RuntimeEvents, RuntimeFuture, SessionControl,
};
use serde::Deserialize;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::{BTreeMap, VecDeque},
    io::Write,
    path::PathBuf,
    process::Stdio,
    sync::{
        Arc, Mutex as StdMutex,
        atomic::{AtomicBool, Ordering},
    },
    time::Duration,
};
use tokio::{
    process::Command,
    sync::{Mutex, Notify, OwnedSemaphorePermit, Semaphore, mpsc, oneshot, watch},
};

const VERSION: &str = "0.160.1";
const MAX_CONTEXT: usize = 32 * 1024 * 1024;
const CONTEXT_TTL: Duration = Duration::from_secs(300);
const DEADLINE: Duration = Duration::from_secs(30);

#[derive(Debug)]
enum NativeFailure {
    CancelledBeforeDispatch,
    StaleAttachment,
    Disconnected,
    Capacity,
}
impl std::fmt::Display for NativeFailure {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            Self::CancelledBeforeDispatch => "command cancelled before dispatch",
            Self::StaleAttachment => "stale native attachment",
            Self::Disconnected => "native_disconnected",
            Self::Capacity => "host command/control capacity or byte budget exhausted",
        })
    }
}
impl std::error::Error for NativeFailure {}
#[derive(Debug)]
struct NativeRejected;
impl std::fmt::Display for NativeRejected {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("Agent rejected the command")
    }
}
impl std::error::Error for NativeRejected {}

#[derive(Clone, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Config {
    pub provider: String,
    pub executable: PathBuf,
    pub sha256: String,
    #[serde(default = "default_close_timeout_ms")]
    pub close_timeout_ms: u64,
    #[serde(default = "default_fact_capacity")]
    pub fact_lane_capacity: usize,
    #[serde(default = "default_sdk_event_bytes")]
    pub sdk_event_bytes: usize,
    #[serde(default)]
    pub env: BTreeMap<String, String>,
}
/// File registration parameters. Native launch details stay inside this adapter.
#[derive(Clone, Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct RegistrationConfig {
    pub command: PathBuf,
    pub sha256: String,
    #[serde(default)]
    pub args: Vec<String>,
    #[serde(default)]
    pub env: BTreeMap<String, String>,
    #[serde(default)]
    pub secret_refs: BTreeMap<String, String>,
    #[serde(default = "default_close_timeout_ms")]
    pub close_timeout_ms: u64,
    #[serde(default = "default_fact_capacity")]
    pub fact_lane_capacity: usize,
    #[serde(default = "default_sdk_event_bytes")]
    pub sdk_event_bytes: usize,
}
impl RegistrationConfig {
    pub fn validate(&self) -> Result<()> {
        ensure!(
            self.command.is_absolute() && !self.command.as_os_str().as_encoded_bytes().contains(&0),
            "command must be an absolute valid path"
        );
        ensure!(
            self.sha256.len() == 64 && self.sha256.bytes().all(|b| b.is_ascii_hexdigit()),
            "sha256 must be a 64-digit hex digest"
        );
        ensure!(
            (1..=300_000).contains(&self.close_timeout_ms),
            "closeTimeoutMs must be 1..300000"
        );
        ensure!(
            (1..=64).contains(&self.fact_lane_capacity),
            "factLaneCapacity must be 1..64"
        );
        ensure!(
            (1024..=MAX_CONTEXT).contains(&self.sdk_event_bytes),
            "sdkEventBytes must be 1024..33554432"
        );
        // Only reviewed app-server switches; routing, config overrides and subcommands
        // stay adapter-owned. Never pass a shell or a second --listen through.
        let mut seen = std::collections::BTreeSet::new();
        ensure!(
            self.args.iter().all(|arg| matches!(
                arg.as_str(),
                "--strict-config" | "--analytics-default-enabled"
            ) && seen.insert(arg)),
            "unsupported or conflicting args"
        );
        for key in self.env.keys().chain(self.secret_refs.keys()) {
            ensure!(
                !key.is_empty() && !key.contains(['=', '\0']),
                "invalid environment key"
            );
        }
        ensure!(
            self.env.values().all(|v| !v.contains('\0')),
            "invalid environment value"
        );
        for (key, reference) in &self.secret_refs {
            ensure!(!self.env.contains_key(key), "env and secretRefs overlap");
            let name = reference.strip_prefix("env:").unwrap_or("");
            ensure!(
                !name.is_empty() && name.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_'),
                "secret reference must use env:NAME"
            );
        }
        Ok(())
    }
    pub fn fingerprint(&self) -> Result<String> {
        self.validate()?;
        let mut canonical = self.clone();
        canonical.sha256.make_ascii_lowercase();
        Ok(format!(
            "{:x}",
            Sha256::digest(serde_json::to_vec(&canonical)?)
        ))
    }
    pub fn runtime(self, observation: Observation) -> ProviderRuntime {
        let mut runtime = ProviderRuntime::new(
            Config {
                provider: "codex".into(),
                executable: self.command,
                sha256: self.sha256.to_ascii_lowercase(),
                close_timeout_ms: self.close_timeout_ms,
                fact_lane_capacity: self.fact_lane_capacity,
                sdk_event_bytes: self.sdk_event_bytes,
                env: self.env,
            },
            observation,
        );
        runtime.secret_refs = self.secret_refs;
        runtime.launch_args = self.args;
        runtime
    }
}

fn default_sdk_event_bytes() -> usize {
    32 * 1024 * 1024
}
fn default_fact_capacity() -> usize {
    64
}
fn default_close_timeout_ms() -> u64 {
    30_000
}

/// Construction-only observation of actual traffic; never generates upstream facts.
#[derive(Clone, Default)]
pub struct Observation {
    pub directory: Option<PathBuf>,
    pub drop_previews: bool,
    pub start_response_delay: Duration,
    pub thread_response_delay: Duration,
    pub thread_response_delay_cwd: Option<PathBuf>,
    pub thread_request_delay: Duration,
    pub approval_response_delay: Duration,
    pub command_write_delay: Duration,
    pub interrupt_response_delay: Duration,
    pub terminal_delay: Duration,
    /// Delay real SDK event delivery in acceptance only; preserves arrival order.
    pub event_delivery_delay: Duration,
    /// Construction-only delay before delivering a complete fact to the real core.
    pub core_write_delay: Duration,
    pub core_write_delay_thread: Option<PathBuf>,
    pub disconnect_interrupt: bool,
    /// Delay delivery of actual closed evidence, without delaying peer facts.
    pub closed_delivery_delay: Duration,
}

struct Recorder(Option<StdMutex<std::fs::File>>, Vec<String>);
impl Recorder {
    fn record(&self, direction: &str, value: &Value) -> Result<()> {
        if let Some(file) = &self.0 {
            let mut value = value.clone();
            redact(&mut value);
            redact_secrets(&mut value, &self.1);
            let mut file = file.lock().unwrap();
            writeln!(
                file,
                "{}",
                json!({"direction":direction,"payload":value,"time":time::OffsetDateTime::now_utc().unix_timestamp_nanos().to_string()})
            )?;
            file.flush()?;
        }
        Ok(())
    }
}
fn redact_secrets(value: &mut Value, secrets: &[String]) {
    match value {
        Value::Object(map) => map.values_mut().for_each(|v| redact_secrets(v, secrets)),
        Value::Array(items) => items.iter_mut().for_each(|v| redact_secrets(v, secrets)),
        Value::String(text) => {
            for secret in secrets {
                *text = text.replace(secret, "[REDACTED]");
            }
        }
        _ => {}
    }
}
fn redact(value: &mut Value) {
    match value {
        Value::Object(map) => {
            for (key, value) in map {
                if [
                    "access_token",
                    "refresh_token",
                    "id_token",
                    "api_key",
                    "authorization",
                    "secret",
                ]
                .contains(&key.to_lowercase().as_str())
                {
                    *value = json!("[REDACTED]");
                } else {
                    redact(value);
                }
            }
        }
        Value::Array(items) => items.iter_mut().for_each(redact),
        Value::String(text) => {
            for (key, secret) in std::env::vars() {
                if (key.contains("KEY") || key.contains("TOKEN") || key.contains("SECRET"))
                    && secret.len() > 8
                {
                    *text = text.replace(&secret, "[REDACTED]");
                }
            }
        }
        _ => {}
    }
}

pub struct ProviderRuntime {
    config: Config,
    remote: Option<RemoteRegistrationConfig>,
    observation: Observation,
    secret_refs: BTreeMap<String, String>,
    launch_args: Vec<String>,
    phase: StdMutex<(&'static str, bool)>,
    host: Mutex<Option<Arc<Host>>>,
    shutdown: AtomicBool,
}
impl ProviderRuntime {
    pub fn new(config: Config, observation: Observation) -> Self {
        Self {
            config,
            remote: None,
            observation,
            secret_refs: BTreeMap::new(),
            launch_args: Vec::new(),
            phase: StdMutex::new(("pending", false)),
            host: Mutex::new(None),
            shutdown: AtomicBool::new(false),
        }
    }
    fn environment(&self) -> Result<BTreeMap<String, String>> {
        let mut environment = self.config.env.clone();
        for (key, reference) in &self.secret_refs {
            let name = reference
                .strip_prefix("env:")
                .context("invalid secret reference")?;
            let value =
                std::env::var(name).map_err(|_| anyhow::anyhow!("secret reference unavailable"))?;
            ensure!(!value.contains('\0'), "secret reference invalid");
            environment.insert(key.clone(), value);
        }
        Ok(environment)
    }
    pub fn provider(&self) -> &str {
        &self.config.provider
    }
    pub async fn status(&self) -> Value {
        let (phase, failed) = *self.phase.lock().unwrap();
        let unavailable = self.host.try_lock().ok().is_some_and(|host| {
            host.as_ref()
                .is_some_and(|h| !h.online.load(Ordering::Acquire))
        });
        let phase = if unavailable && phase == "ready" {
            "error"
        } else {
            phase
        };
        json!({"phase":phase,"unavailableReason": if failed || phase == "error" { json!({"reason":"runtime_unavailable","message":"Runtime activation or connection is unavailable"}) } else { Value::Null }})
    }
    pub async fn start(&self) -> Result<Arc<Host>> {
        *self.phase.lock().unwrap() = ("starting", false);
        let result = self.start_inner().await;
        *self.phase.lock().unwrap() = if result.is_ok() {
            ("ready", false)
        } else {
            ("error", true)
        };
        result
    }
    async fn start_inner(&self) -> Result<Arc<Host>> {
        ensure!(!self.shutdown.load(Ordering::Acquire), "runtime is stopped");
        let mut slot = self.host.lock().await;
        if let Some(host) = slot.as_ref().filter(|h| h.online.load(Ordering::Acquire)) {
            let host = host.clone();
            drop(slot);
            host.wait_initialized().await?;
            return Ok(host);
        }
        if let Some(old) = slot.as_ref() {
            loop {
                let done = old.done.notified();
                if old.stopped.load(Ordering::Acquire) {
                    break;
                }
                done.await;
            }
        }
        ensure!(
            slot.as_ref()
                .is_none_or(|host| host.local_resources_ended.load(Ordering::Acquire)),
            "previous host release is unknown"
        );
        ensure!(!self.shutdown.load(Ordering::Acquire), "runtime is stopped");
        ensure!(self.config.provider == "codex", "unsupported provider");
        ensure!(
            (1024..=MAX_CONTEXT).contains(&self.config.sdk_event_bytes),
            "sdkEventBytes must be 1024..33554432"
        );
        ensure!(
            (1..=64).contains(&self.config.fact_lane_capacity),
            "factLaneCapacity must be 1..64"
        );
        ensure!(
            (1..=300_000).contains(&self.config.close_timeout_ms),
            "closeTimeoutMs must be 1..300000"
        );
        let mut local = if self.remote.is_none() {
            Some(self.acquire_local().await?)
        } else {
            None
        };
        let endpoint = if let Some(remote) = &self.remote {
            remote.endpoint()?
        } else {
            RemoteAppServerEndpoint::UnixSocket {
                socket_path: local.as_ref().unwrap().socket.clone().try_into()?,
            }
        };
        let recovery_scope = local.as_ref().and_then(|r| r.recovery_scope.clone());
        let generation = uuid::Uuid::now_v7().to_string();
        let identity = if let Some(local) = &local {
            json!({"provider":"codex","transport":"app_server_unix_websocket","version":VERSION,"connectorVersion":"2","activationModel":"runtime_scoped_server","executable":self.config.executable,"sha256":local.hash,"pid":local.child.id(),"hostGeneration":generation,"sdkRevision":"d27764b82f7118f674371e6d6e76271d9d606edb","sdkPatchSha256":format!("{:x}",Sha256::digest(include_bytes!("../../vendor/codex-app-server-client/MASS.patch")))})
        } else {
            // No address, credentials, or inferred service/account/data identity.
            json!({"provider":"codex","transport":"app_server_remote_websocket","connectorVersion":"2","activationModel":"connection_only","hostGeneration":generation})
        };
        let directory = self
            .observation
            .directory
            .as_ref()
            .map(|p| p.join(&generation));
        let secrets = match &endpoint {
            RemoteAppServerEndpoint::WebSocket {
                auth_token: Some(token),
                ..
            } => vec![token.clone()],
            _ => vec![],
        };
        let recorder = Arc::new(Recorder(
            if let Some(dir) = &directory {
                std::fs::create_dir_all(dir)?;
                std::fs::write(
                    dir.join("spawn.json"),
                    serde_json::to_vec_pretty(
                        &json!({"runtime":identity,"cwd":if local.is_some() { json!("/") } else { Value::Null },"argv":local.as_ref().map(|r|json!(["app-server","--listen",format!("unix://{}",r.socket.display()),"-c","thread_unload_delay_secs=0"])),"resourceOwnership":if local.is_some() { "owned_process" } else { "connection_only" },"observationBoundary":"official_client_api","sdkConsumerQueue":"bounded_patched_official","sdkEventCount":32,"sdkEventBytes":self.config.sdk_event_bytes,"sdkFrameBytes":134217728}),
                    )?,
                )?;
                Some(StdMutex::new(std::fs::File::create(
                    dir.join("wire.jsonl"),
                )?))
            } else {
                None
            },
            secrets,
        ));
        let (tx, mut rx) = mpsc::channel::<QueuedCommand>(32);
        let (control, mut controls) = mpsc::channel::<QueuedCommand>(32);
        let routes = Arc::new(StdMutex::new(BTreeMap::<String, Arc<SessionHandle>>::new()));
        let online = Arc::new(AtomicBool::new(true));
        let host = Arc::new(Host {
            tx,
            control,
            control_capacity: Arc::new(Semaphore::new(32)),
            command_capacity: Arc::new(Semaphore::new(32)),
            attachment_capacity: Arc::new(Semaphore::new(128)),
            unresolved_opens: StdMutex::new(Vec::new()),
            routes: routes.clone(),
            context_capacity: Arc::new(Semaphore::new(128 * 1024 * 1024)),
            online: online.clone(),
            accepting: AtomicBool::new(true),
            identity,
            recovery_scope,
            owns_process: local.is_some(),
            generation,
            observation: self.observation.clone(),
            recorder: recorder.clone(),
            fact_lane_capacity: self.config.fact_lane_capacity,
            sdk_event_bytes: self.config.sdk_event_bytes,
            close_timeout: Duration::from_millis(self.config.close_timeout_ms),
            initialized: AtomicBool::new(false),
            initialization_error: StdMutex::new(None),
            ready: Notify::new(),
            stopping: AtomicBool::new(false),
            stop: Notify::new(),
            stopped: AtomicBool::new(false),
            local_resources_ended: AtomicBool::new(false),
            done: Notify::new(),
        });
        *slot = Some(host.clone());
        let active = host.clone();
        tokio::spawn(async move {
            let connected = {
                let connecting = async {
                    if let Some(local) = &mut local {
                        while !local.socket.exists() {
                            ensure!(
                                local.child.try_wait()?.is_none(),
                                "native exited before Unix socket initialization"
                            );
                            tokio::time::sleep(Duration::from_millis(10)).await;
                        }
                    }
                    let remote = RemoteAppServerClient::connect(RemoteAppServerConnectArgs {
                        endpoint,
                        client_name: "mass".into(),
                        client_version: "0.1.0".into(),
                        experimental_api: true,
                        mcp_server_openai_form_elicitation: false,
                        opt_out_notification_methods: vec![],
                        channel_capacity: 32,
                        event_byte_capacity: active.sdk_event_bytes,
                        max_response_bytes: 2 * 1024 * 1024,
                    })
                    .await?;
                    if !active.owns_process && remote.server_version() != Some(VERSION) {
                        let _ = remote.shutdown().await;
                        bail!("unsupported Remote protocol version");
                    }
                    Ok::<_, anyhow::Error>(AppServerClient::Remote(remote))
                };
                if active.stopping.load(Ordering::Acquire) {
                    Err(anyhow::anyhow!("runtime stopping"))
                } else {
                    tokio::select! {
                        biased;
                        _=active.stop.notified()=>Err(anyhow::anyhow!("runtime stopping")),
                        result=tokio::time::timeout(DEADLINE,connecting)=>match result {Ok(result)=>result,Err(_)=>Err(anyhow::anyhow!("official SDK initialize timeout"))},
                    }
                }
            };
            let mut sdk = match connected {
                Ok(sdk) => Some(sdk),
                Err(error) => {
                    *active.initialization_error.lock().unwrap() = Some(if active.owns_process {
                        error.to_string()
                    } else {
                        "Remote connection or handshake failed".into()
                    });
                    None
                }
            };
            active.initialized.store(true, Ordering::Release);
            active.ready.notify_waiters();
            let mut requests = FuturesUnordered::new();
            let mut commands = CommandQueue::default();
            let mut callbacks = BTreeMap::<String, Arc<PendingApproval>>::new();
            let mut grants = VecDeque::<Arc<PendingApproval>>::new();
            let mut deferred_events =
                VecDeque::<(tokio::time::Instant, Value, usize, OwnedSemaphorePermit)>::new();
            let mut deferred_event_bytes = 0usize;
            let mut expiry = tokio::time::interval(Duration::from_millis(25));
            let mut next = 0u64;
            let result:Result<()>=async {
                let sdk=sdk.as_mut().context("official SDK initialization failed")?;
                recorder.record("local",&json!({"method":"sdk/initialized","revision":"d27764b82f7118f674371e6d6e76271d9d606edb","handshakeOwner":"codex-app-server-client"}))?;
                loop {
                    if active.stopping.load(Ordering::Acquire) { break; }
                    let ready_event=deferred_events.front().is_some_and(|(at,_,_,_)|*at<=tokio::time::Instant::now());
                    let ready_grant=grants.iter().position(|a|a.respond_at<=tokio::time::Instant::now());
                    tokio::select! {
                        _=active.stop.notified()=>break,
                        command=controls.recv()=> { if let Some(command)=command {commands.push_control(command);} },
                        _=requests.next(), if !requests.is_empty()=> {},
                        _=expiry.tick()=> {
                            let handles:Vec<_>=routes.lock().unwrap().values().cloned().collect();
                            for handle in handles {
                                if let Err(error)=handle.observe_deferred().await {handle.disconnect(&error.to_string()).await;}
                                if handle.closed.load(Ordering::Acquire) && !handle.released.load(Ordering::Acquire) && !handle.close_requested.swap(true,Ordering::AcqRel) {
                                    let failed=handle.clone();
                                    requests.push(async move {
                                        let result=failed.host.request_observed("thread/unsubscribe",json!({"threadId":failed.thread}),None,Some(failed.owner())).await;
                                        if result.as_ref().is_ok_and(|v|v["status"]=="notLoaded") {failed.released.store(true,Ordering::Release);}
                                        if result.as_ref().is_err_and(|e|matches!(e.downcast_ref::<NativeFailure>(),Some(NativeFailure::Capacity))) {failed.close_requested.store(false,Ordering::Release);}
                                        failed.changed.notify_waiters();
                                    }.boxed());
                                }
                                if handle.closed.load(Ordering::Acquire) && handle.released.load(Ordering::Acquire) && handle.events.is_drained() {
                                    let mut routes=routes.lock().unwrap();
                                    if routes.get(&handle.thread).is_some_and(|h|h.connection_id==handle.connection_id) {routes.remove(&handle.thread);}
                                    handle.attachment_slot.lock().unwrap().take();
                                }
                                let expired=handle.state.lock().unwrap().tools.values().any(|c|c.expires<=tokio::time::Instant::now()) || callbacks.values().any(|a|a.owner.connection_id==handle.connection_id && a.expires<=tokio::time::Instant::now());
                                if expired {handle.disconnect("native tool/approval settlement timeout").await;}
                            }
                            callbacks.retain(|_,a|routes.lock().unwrap().get(&a.owner.thread).is_some_and(|h|h.online()));
                        },
                        _=std::future::ready(()), if ready_grant.is_some()=> {
                            let approval=grants.remove(ready_grant.expect("ready approval")).expect("queued approval");
                            let handle=routes.lock().unwrap().get(&approval.owner.thread).cloned();
                            let _publication=if let Some(h)=&handle {Some(h.publication.lock().await)} else {None};
                            let current=callbacks.get(&approval.id.to_string()).is_some_and(|a|Arc::ptr_eq(a,&approval));
                            let valid=current && handle.as_ref().is_some_and(|h|h.command_valid(&approval.owner,false) && h.approval_valid(&approval.turn,&approval.item));
                            if current && !approval.response_written.load(Ordering::Acquire) {
                                let id:RequestId=serde_json::from_value(approval.id.clone())?;
                                if valid {
                                    let answer:CommandExecutionRequestApprovalResponse=serde_json::from_value(json!({"decision":"accept"}))?;
                                    let answer=serde_json::to_value(answer)?;
                                    tokio::time::timeout(Duration::from_secs(2),sdk.resolve_server_request(id,answer.clone())).await.context("official approval write deadline")??;
                                    recorder.record("client",&json!({"id":approval.id,"result":answer}))?;
                                } else {
                                    let error=JSONRPCErrorError {code:-32602,message:"stale native approval".into(),data:None};
                                    tokio::time::timeout(Duration::from_secs(2),sdk.reject_server_request(id,error.clone())).await.context("official rejection write deadline")??;
                                    recorder.record("client",&json!({"id":approval.id,"error":error}))?;
                                }
                                approval.response_written.store(true,Ordering::Release);
                            } else {
                                recorder.record("local",&json!({"method":"approval/staleDecisionSuppressed","id":approval.id,"threadId":approval.owner.thread,"cancelGeneration":approval.owner.cancel_generation}))?;
                            }
                            if let Some(handle)=handle.as_ref() {handle.policy_settled();}
                        },
                        _=std::future::ready(()), if ready_event=> {
                            let (_,value,bytes,_permit)=deferred_events.pop_front().expect("ready SDK event");
                            deferred_event_bytes-=bytes;
                            dispatch_sdk_event(&active,sdk,&recorder,&mut callbacks,&mut grants,value).await?;
                        },
                        command=rx.recv()=>match command {Some(command)=>commands.push(command),None=>break},
                        _=std::future::ready(()), if !commands.is_empty()=> {
                            while let Ok(command)=rx.try_recv() {commands.push(command);}
                            let queued=commands.pop();
                            match queued.command {
                                HostCommand::Request {method,params,reply,sent,owner,..}=> {
                                    if owner.is_none() && !active.accepting.load(Ordering::Acquire) {let _=reply.send(Err(NativeFailure::Disconnected.into()));continue;}
                                    let handle=owner.as_ref().and_then(|o|routes.lock().unwrap().get(&o.thread).cloned());
                                    let _publication=if let Some(h)=&handle {Some(h.publication.lock().await)} else {None};
                                    if let Some(owner)=&owner {
                                        let valid=handle.as_ref().is_some_and(|h|if method=="thread/unsubscribe" {h.connection_id==owner.connection_id} else {h.command_valid(owner,method=="turn/interrupt")});
                                        if !valid {
                                            let cancelled=handle.as_ref().is_some_and(|h|h.online() && h.connection_id==owner.connection_id && h.host.generation==owner.host_generation);
                                            let failure=if cancelled {NativeFailure::CancelledBeforeDispatch} else {NativeFailure::StaleAttachment};
                                            let _=reply.send(Err(failure.into()));continue;
                                        }
                                    }
                                    next+=1;
                                    let request=sdk_request(method,params,RequestId::String(next.to_string()))?;
                                    let serialized=serde_json::to_value(&request)?;
                                    let request_handle=sdk.request_handle();
                                    let mut pending=Box::pin(async move {request_handle.request(request).await});
                                    // The official client owns publication after this API handoff.
                                    // Never hold MASS admission while waiting for a native RPC ACK.
                                    let first=futures_util::poll!(pending.as_mut());
                                    recorder.record("client",&serialized)?;
                                    let observed=recorder.clone();let observation=active.observation.clone();
                                    requests.push(async move {
                                        let _permit=queued._permit;
                                        let _bytes=queued._bytes;
                                        let response=match first {std::task::Poll::Ready(r)=>r,std::task::Poll::Pending=>pending.await};
                                        if response.is_ok() && let Some(sent)=&sent {sent.store(true,Ordering::Release);}
                                        let result=match response {
                                            Ok(Ok(value))=> {let _=observed.record("server",&json!({"id":serialized["id"],"result":value}));Ok(value)},
                                            Ok(Err(error))=> {let _=observed.record("server",&json!({"id":serialized["id"],"error":error}));Err(NativeRejected.into())},
                                            Err(_)=>Err(NativeFailure::Disconnected.into()),
                                        };
                                        let delay=if method=="turn/interrupt" {observation.interrupt_response_delay} else if method=="turn/start" {observation.start_response_delay} else if method=="thread/start" && result.as_ref().is_ok_and(|v|observation.thread_response_delay_cwd.as_ref().is_none_or(|cwd|v["thread"]["cwd"]==cwd.to_string_lossy().as_ref())) {observation.thread_response_delay} else {Duration::ZERO};
                                        tokio::time::sleep(delay).await;let _=reply.send(result);
                                    }.boxed());
                                    ensure!(!(method=="turn/interrupt" && active.observation.disconnect_interrupt),"controlled official SDK disconnect after interrupt API handoff");
                                },

                            }
                        },
                        event=sdk.next_event()=> {
                            let value=match event.context("official SDK event stream closed")? {
                                AppServerEvent::ServerNotification(value)=>serde_json::to_value(value)?,
                                AppServerEvent::ServerRequest(value)=>serde_json::to_value(value)?,
                                AppServerEvent::NotificationDecodeError {method,thread_id,message}=> {
                                    recorder.record("local",&json!({"method":"sdk/notificationDecodeError","nativeMethod":method,"threadId":thread_id,"message":message}))?;
                                    if let Some(thread)=thread_id {
                                        let handle=routes.lock().unwrap().get(&thread).cloned();
                                        if let Some(handle)=handle {handle.disconnect(&message).await;} else {
                                            recorder.record("local",&json!({"method":"sdk/staleNotificationDecodeError","threadId":thread,"nativeMethod":method}))?;
                                        }
                                        continue;
                                    }
                                    bail!("unattributable typed notification failure: {message}");
                                },
                                AppServerEvent::UnsupportedNotification {method}=> {recorder.record("local",&json!({"method":"sdk/unsupportedNotification","nativeMethod":method}))?;continue;},
                                AppServerEvent::Disconnected {message}=> { if active.owns_process { bail!("native_disconnected: {message}"); } else { bail!("Remote connection lost; remote work outcome unknown"); } },
                                AppServerEvent::Lagged {skipped}=>bail!("official SDK lost {skipped} events"),
                            };
                            recorder.record("server",&value)?;
                            if active.observation.event_delivery_delay.is_zero() {
                                dispatch_sdk_event(&active,sdk,&recorder,&mut callbacks,&mut grants,value).await?;
                            } else {
                                // A bounded construction-only gate retains actual SDK facts.
                                // Requests and cancellation control continue while facts wait.
                                ensure!(deferred_events.len()<128,"acceptance event gate capacity");
                                let bytes=serde_json::to_vec(&value)?.len();
                                ensure!(bytes<=MAX_CONTEXT.saturating_sub(deferred_event_bytes),"acceptance event gate byte limit");
                                let permit=active.context_capacity.clone().try_acquire_many_owned(bytes as u32).context("host deferred observation byte limit")?;
                                deferred_event_bytes+=bytes;
                                deferred_events.push_back((tokio::time::Instant::now()+active.observation.event_delivery_delay,value,bytes,permit));
                            }
                        }
                    }
                }
                Ok(())
            }.await;
            online.store(false, Ordering::Release);
            drop(requests);
            drop(commands);
            drop(rx);
            drop(controls);
            let handles: Vec<_> = routes.lock().unwrap().values().cloned().collect();
            for handle in &handles {
                handle.invalidate(
                    &result
                        .as_ref()
                        .err()
                        .map_or("runtime stopped".to_owned(), ToString::to_string),
                );
            }
            let draining = async {
                for handle in &handles {
                    handle.events.drain().await;
                }
            };
            tokio::pin!(draining);
            if let Some(client) = sdk.as_mut() {
                let mut events_open = true;
                loop {
                    tokio::select! {
                        _=&mut draining=>break,
                        event=client.next_event(), if events_open=> {
                            let value=match event {
                                Some(AppServerEvent::ServerNotification(v))=>serde_json::to_value(v).ok(),
                                Some(AppServerEvent::ServerRequest(v))=>serde_json::to_value(v).ok(),
                                Some(AppServerEvent::UnsupportedNotification{method})=>Some(json!({"unsupportedNotification":method})),
                                Some(AppServerEvent::NotificationDecodeError{method,thread_id,message})=>Some(json!({"decodeError":message,"method":method,"threadId":thread_id})),
                                _=>{events_open=false;None},
                            };
                            if let Some(value)=value {let _=recorder.record("shutdown_uncommitted",&value);}
                        }
                    }
                }
            } else {
                draining.await;
            }
            if let Some(AppServerClient::Remote(client)) = &sdk {
                let (count, bytes) = client.event_buffer_high_water();
                let _=recorder.record("local",&json!({"method":"sdk/eventBufferHighWater","count":count,"bytes":bytes,"countLimit":32,"byteLimit":active.sdk_event_bytes,"frameLimit":134217728,"rpcResponseLimit":2097152}));
            }
            if let Some(sdk) = sdk
                && let Err(error) = sdk.shutdown().await
            {
                let _ = error;
                tracing::warn!("official SDK shutdown completed with error");
            }
            // Only a reaped child proves remote work disappeared. Closing a Remote
            // connection releases our local worker, never its unknown server work.
            let (local_ended, process_reaped) = if let Some(local) = local {
                let reaped = local.reap().await;
                (reaped, reaped)
            } else {
                (true, false)
            };
            if process_reaped {
                routes.lock().unwrap().clear();
            }
            for handle in &handles {
                if process_reaped {
                    handle.released.store(true, Ordering::Release);
                    handle.attachment_slot.lock().unwrap().take();
                    handle.provisional_owner.lock().unwrap().take();
                }
                handle.changed.notify_waiters();
            }
            active
                .local_resources_ended
                .store(local_ended, Ordering::Release);
            if process_reaped {
                active.unresolved_opens.lock().unwrap().clear();
            }
            if let Some(dir) = directory {
                let mut cleanup = json!({"localConnectionEnded":local_ended,
                    "ownedProcess":active.owns_process,"processReaped":process_reaped,
                    "remoteWorkOutcome":if process_reaped { "ended" } else { "unknown" }});
                if active.owns_process {
                    cleanup["reaped"] = json!(process_reaped);
                }
                let _ = std::fs::write(dir.join("cleanup.json"), cleanup.to_string());
            }
            active.stopped.store(true, Ordering::Release);
            active.done.notify_waiters();
        });
        drop(slot);
        host.wait_initialized().await?;
        Ok(host)
    }
    async fn acquire_local(&self) -> Result<LocalResources> {
        ensure!(
            self.config.executable.is_absolute(),
            "native executable must be absolute"
        );
        let hash = format!(
            "{:x}",
            Sha256::digest(std::fs::read(&self.config.executable)?)
        );
        ensure!(
            hash == self.config.sha256,
            "native binary SHA256 differs from reviewed identity"
        );
        let environment = self.environment()?;
        let recovery_scope = recovery::scope(&environment);
        let version = tokio::time::timeout(
            DEADLINE,
            Command::new(&self.config.executable)
                .arg("--version")
                .envs(&environment)
                .kill_on_drop(true)
                .output(),
        )
        .await??;
        ensure!(
            version.status.success()
                && String::from_utf8_lossy(&version.stdout).trim()
                    == format!("codex-cli {VERSION}"),
            "unsupported native version"
        );
        let socket_directory = tempfile::Builder::new()
            .prefix("mass-codex-")
            .tempdir_in("/tmp")?;
        let socket = socket_directory.path().join("rpc.sock");
        let endpoint = format!("unix://{}", socket.display());
        let mut child = Command::new(&self.config.executable)
            .args([
                "app-server",
                "--listen",
                &endpoint,
                "-c",
                "thread_unload_delay_secs=0",
            ])
            .args(&self.launch_args)
            .current_dir("/")
            .envs(&environment)
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::piped())
            .kill_on_drop(true)
            .spawn()?;
        let stderr = child.stderr.take().context("native stderr")?;
        let stderr_task = tokio::spawn(async move {
            let mut stderr = stderr;
            tokio::io::copy(&mut stderr, &mut tokio::io::sink()).await
        });
        Ok(LocalResources {
            child,
            socket_directory,
            socket,
            stderr_task,
            hash,
            recovery_scope,
        })
    }
    pub async fn close_admission(&self) {
        self.shutdown.store(true, Ordering::Release);
        if let Some(host) = self.host.lock().await.as_ref() {
            host.accepting.store(false, Ordering::Release);
        }
    }
    pub async fn stop(&self) {
        *self.phase.lock().unwrap() = ("stopping", false);
        self.close_admission().await;
        if let Some(host) = self.host.lock().await.as_ref() {
            host.stopping.store(true, Ordering::Release);
            host.stop.notify_one();
            // Core file writes have no finite wall-clock guarantee. Native cleanup
            // deadlines begin after accepted core writes, not around blocking I/O.
            loop {
                let done = host.done.notified();
                if host.stopped.load(Ordering::Acquire) {
                    break;
                }
                done.await;
            }
        }
        let released = self
            .host
            .lock()
            .await
            .as_ref()
            .is_none_or(|host| host.local_resources_ended.load(Ordering::Acquire));
        *self.phase.lock().unwrap() = if released {
            ("stopped", false)
        } else {
            ("error", true)
        };
    }
}
async fn dispatch_sdk_event(
    host: &Host,
    sdk: &AppServerClient,
    recorder: &Recorder,
    callbacks: &mut BTreeMap<String, Arc<PendingApproval>>,
    grants: &mut VecDeque<Arc<PendingApproval>>,
    value: Value,
) -> Result<()> {
    let route = value["params"]["threadId"]
        .as_str()
        .and_then(|id| host.routes.lock().unwrap().get(id).cloned());
    if serde_json::to_vec(&value)?.len() > MAX_CONTEXT {
        if let Some(handle) = route {
            handle
                .disconnect("typed event exceeds 32 MiB application limit; history incomplete")
                .await;
            return Ok(());
        }
        bail!("host event exceeds 32 MiB application limit");
    }
    if value.get("id").is_some() {
        let registered = route.as_ref().and_then(|handle| {
            if value["method"] != "item/commandExecution/requestApproval" {
                return None;
            }
            match handle.register_approval(&value, callbacks) {
                Ok(a) => Some(a),
                Err(error) => {
                    tracing::warn!(%error,"native approval rejected");
                    None
                }
            }
        });
        if let Some(approval) = registered {
            let approval = Arc::new(approval);
            callbacks.insert(value["id"].to_string(), approval.clone());
            grants.push_back(approval);
        } else {
            let id = serde_json::from_value(value["id"].clone())?;
            let error = JSONRPCErrorError {
                code: -32601,
                message: "unsupported_capability: unsupported or invalid native control request"
                    .into(),
                data: None,
            };
            tokio::time::timeout(
                Duration::from_secs(2),
                sdk.reject_server_request(id, error.clone()),
            )
            .await
            .context("official rejection write deadline")??;
            recorder.record("client", &json!({"id":value["id"],"error":error}))?;
        }
    } else {
        if value["method"] == "serverRequest/resolved" {
            let key = value["params"]["requestId"].to_string();
            if callbacks
                .get(&key)
                .is_some_and(|a| value["params"]["threadId"] == a.owner.thread)
                && let Some(approval) = callbacks.remove(&key)
                && let Some(handle) = host
                    .routes
                    .lock()
                    .unwrap()
                    .get(&approval.owner.thread)
                    .cloned()
            {
                handle.approval_settled();
            }
        }
        if let Some(handle) = route {
            let delay = match value["method"].as_str() {
                Some("turn/completed") => host.observation.terminal_delay,
                Some("thread/closed") => host.observation.closed_delivery_delay,
                _ => Duration::ZERO,
            };
            if !delay.is_zero() {
                handle.defer(value, delay)?;
            } else if let Err(error) = handle.observe(&value).await {
                handle.disconnect(&error.to_string()).await;
            }
        }
    }
    Ok(())
}

fn sdk_request(method: &str, params: Value, request_id: RequestId) -> Result<ClientRequest> {
    Ok(match method {
        "thread/start" => ClientRequest::ThreadStart {
            request_id,
            params: ThreadStartParams {
                cwd: params["cwd"].as_str().map(str::to_owned),
                approval_policy: Some(serde_json::from_value(json!("untrusted"))?),
                approvals_reviewer: Some(serde_json::from_value(json!("user"))?),
                sandbox: Some(serde_json::from_value(json!("workspace-write"))?),
                ephemeral: Some(false),
                history_mode: Some(serde_json::from_value(json!("paginated"))?),
                ..Default::default()
            },
        },
        "turn/start" => ClientRequest::TurnStart {
            request_id,
            params: TurnStartParams {
                thread_id: params["threadId"]
                    .as_str()
                    .context("missing thread")?
                    .into(),
                client_user_message_id: params["clientUserMessageId"].as_str().map(str::to_owned),
                input: serde_json::from_value(params["input"].clone())?,
                ..Default::default()
            },
        },
        "thread/resume" => ClientRequest::ThreadResume {
            request_id,
            params: serde_json::from_value::<ThreadResumeParams>(params)?,
        },
        "turn/interrupt" => ClientRequest::TurnInterrupt {
            request_id,
            params: TurnInterruptParams {
                thread_id: params["threadId"]
                    .as_str()
                    .context("missing thread")?
                    .into(),
                turn_id: params["turnId"].as_str().context("missing turn")?.into(),
            },
        },
        "thread/unsubscribe" => ClientRequest::ThreadUnsubscribe {
            request_id,
            params: ThreadUnsubscribeParams {
                thread_id: params["threadId"]
                    .as_str()
                    .context("missing thread")?
                    .into(),
            },
        },
        _ => bail!("unsupported official client operation"),
    })
}

#[derive(Clone)]
struct AttachmentOwner {
    host_generation: String,
    cancel_generation: u64,
    thread: String,
    connection_id: String,
}
enum HostCommand {
    Request {
        method: &'static str,
        params: Value,
        reply: oneshot::Sender<Result<Value>>,
        sent: Option<Arc<AtomicBool>>,
        owner: Option<AttachmentOwner>,
        lane: String,
    },
}
struct QueuedCommand {
    command: HostCommand,
    _permit: OwnedSemaphorePermit,
    _bytes: OwnedSemaphorePermit,
}
// A single bounded budget covers ingress and round-robin lanes together. A busy
// attachment cannot drain its entire lane ahead of another ready attachment.
#[derive(Default)]
struct CommandQueue(VecDeque<(String, VecDeque<QueuedCommand>)>);
impl CommandQueue {
    fn push_control(&mut self, command: QueuedCommand) {
        self.0
            .push_front((String::new(), VecDeque::from([command])));
    }
    fn push(&mut self, command: QueuedCommand) {
        let HostCommand::Request { lane, .. } = &command.command;
        let lane = lane.clone();
        if let Some((_, queue)) = self.0.iter_mut().find(|(key, _)| *key == lane) {
            queue.push_back(command);
        } else {
            self.0.push_back((lane, VecDeque::from([command])));
        }
    }
    fn pop(&mut self) -> QueuedCommand {
        let (lane, mut queue) = self.0.pop_front().expect("ready command lane");
        let command = queue.pop_front().expect("nonempty command lane");
        if !queue.is_empty() {
            self.0.push_back((lane, queue));
        }
        command
    }
    fn is_empty(&self) -> bool {
        self.0.is_empty()
    }
}
pub struct Host {
    tx: mpsc::Sender<QueuedCommand>,
    control: mpsc::Sender<QueuedCommand>,
    control_capacity: Arc<Semaphore>,
    command_capacity: Arc<Semaphore>,
    attachment_capacity: Arc<Semaphore>,
    unresolved_opens: StdMutex<Vec<Arc<dyn Send + Sync>>>,
    context_capacity: Arc<Semaphore>,
    routes: Arc<StdMutex<BTreeMap<String, Arc<SessionHandle>>>>,
    online: Arc<AtomicBool>,
    accepting: AtomicBool,
    pub identity: Value,
    recovery_scope: Option<String>,
    owns_process: bool,
    generation: String,
    observation: Observation,
    recorder: Arc<Recorder>,
    fact_lane_capacity: usize,
    sdk_event_bytes: usize,
    close_timeout: Duration,
    initialized: AtomicBool,
    initialization_error: StdMutex<Option<String>>,
    ready: Notify,
    stopping: AtomicBool,
    stop: Notify,
    stopped: AtomicBool,
    local_resources_ended: AtomicBool,
    done: Notify,
}
impl Host {
    fn enqueue_command(&self, command: HostCommand, control: bool) -> Result<()> {
        ensure!(
            control || self.accepting.load(Ordering::Acquire),
            "runtime admission closed"
        );
        let capacity = if control {
            &self.control_capacity
        } else {
            &self.command_capacity
        };
        let permit = capacity
            .clone()
            .try_acquire_owned()
            .map_err(|_| NativeFailure::Capacity)?;
        let HostCommand::Request { params, .. } = &command;
        let bytes = serde_json::to_vec(params)?.len();
        ensure!(bytes <= 1024 * 1024, "native command exceeds 1 MiB limit");
        let bytes = self
            .context_capacity
            .clone()
            .try_acquire_many_owned(bytes as u32)
            .map_err(|_| NativeFailure::Capacity)?;
        let sender = if control { &self.control } else { &self.tx };
        sender
            .try_send(QueuedCommand {
                command,
                _permit: permit,
                _bytes: bytes,
            })
            .context("native command lane unavailable")?;
        Ok(())
    }
    async fn enqueue(&self, command: HostCommand) -> Result<()> {
        self.enqueue_command(command, false)
    }
    async fn wait_initialized(&self) -> Result<()> {
        loop {
            let ready = self.ready.notified();
            if self.initialized.load(Ordering::Acquire) {
                break;
            }
            ready.await;
        }
        if let Some(error) = self.initialization_error.lock().unwrap().as_ref() {
            bail!("native initialization failed: {error}");
        }
        Ok(())
    }
    async fn request_observed(
        &self,
        method: &'static str,
        params: Value,
        sent: Option<Arc<AtomicBool>>,
        owner: Option<AttachmentOwner>,
    ) -> Result<Value> {
        if !self.online.load(Ordering::Acquire) {
            bail!(NativeFailure::Disconnected);
        }
        let (reply, rx) = oneshot::channel();
        if method == "turn/start" {
            tokio::time::sleep(self.observation.command_write_delay).await;
        }
        let command = HostCommand::Request {
            method,
            params,
            reply,
            sent,
            lane: owner.as_ref().map_or_else(
                || uuid::Uuid::now_v7().to_string(),
                |owner| owner.connection_id.clone(),
            ),
            owner,
        };
        self.enqueue_command(command, method == "thread/unsubscribe")?;
        if method == "turn/start" {
            // HTTP acceptance deadlines never abandon actual native settlement.
            rx.await?
        } else {
            tokio::time::timeout(DEADLINE, rx)
                .await
                .context("native request timeout")??
        }
    }
    pub async fn new_session<T: Send + Sync + 'static>(
        self: &Arc<Self>,
        cwd: PathBuf,
        events: Arc<Mutex<dyn RuntimeEvents>>,
        connection_id: String,
        deadline: tokio::time::Instant,
        open_owner: Arc<T>,
    ) -> Result<Arc<SessionHandle>> {
        self.open_session(
            cwd,
            None,
            Arc::new(move |_| events.clone()),
            connection_id,
            deadline,
            open_owner,
        )
        .await
    }
    pub async fn resume_session<T: Send + Sync + 'static>(
        self: &Arc<Self>,
        cwd: PathBuf,
        native_id: String,
        events: Arc<Mutex<dyn RuntimeEvents>>,
        connection_id: String,
        deadline: tokio::time::Instant,
        open_owner: Arc<T>,
    ) -> Result<Arc<SessionHandle>> {
        self.open_session(
            cwd,
            Some(native_id),
            Arc::new(move |_| events.clone()),
            connection_id,
            deadline,
            open_owner,
        )
        .await
    }
    fn retain_unresolved(&self, owner: Arc<dyn Send + Sync>) {
        let mut pending = self.unresolved_opens.lock().unwrap();
        if !self.owns_process || !self.local_resources_ended.load(Ordering::Acquire) {
            pending.push(owner);
        }
    }
    async fn open_session(
        self: &Arc<Self>,
        cwd: PathBuf,
        native_id: Option<String>,
        events: mass_runtime::EventFactory,
        connection_id: String,
        deadline: tokio::time::Instant,
        open_owner: Arc<dyn Send + Sync>,
    ) -> Result<Arc<SessionHandle>> {
        if native_id.is_some() && !self.owns_process {
            return Err(remote::recovery_unsupported());
        }
        let events = recovery::events_for(native_id.as_deref(), events);
        let attachment_slot = self
            .attachment_capacity
            .clone()
            .try_acquire_owned()
            .context("host attachment/open limit (128)")?;
        // Construction-only scheduling fault: delay a real request, never invent a reply.
        tokio::time::sleep(self.observation.thread_request_delay).await;
        let (reply, mut response) = oneshot::channel();
        self.enqueue(HostCommand::Request {
            method:if native_id.is_some() {"thread/resume"} else {"thread/start"},
            params:json!({"threadId":native_id,"cwd":cwd,"approvalPolicy":"untrusted","approvalsReviewer":"user","sandbox":"workspace-write","ephemeral":false,"historyMode":"paginated","excludeTurns":true}),
            reply,sent:None,owner:None,lane:connection_id.clone(),
        }).await?;
        let opened = match tokio::time::timeout_at(deadline, &mut response).await {
            Ok(Ok(Ok(value))) => value,
            Ok(Ok(Err(error))) => {
                if error.is::<NativeFailure>() {
                    self.retain_unresolved(open_owner);
                    return Err(mass_runtime::OpenUnknown.into());
                }
                return Err(error);
            }
            Ok(Err(_)) => {
                self.retain_unresolved(open_owner);
                return Err(mass_runtime::OpenUnknown.into());
            }
            Err(_) => {
                // Retain ownership of this open until its actual late result can release
                // the unpublished thread. Other attachments can continue on the host.
                let host = self.clone();
                tokio::spawn(async move {
                    // The service's name reservation outlives the timed-out HTTP
                    // result, until this specific native open has settled.
                    match response.await {
                        Ok(Ok(opened)) => match host
                            .bind(opened, events, connection_id, open_owner, attachment_slot)
                            .await
                        {
                            Ok(handle) => {
                                if let Err(error) = host.rollback(&handle).await {
                                    tracing::warn!(%error,"late provisional thread release unknown");
                                }
                            }
                            Err(error) => {
                                tracing::warn!(%error,"late provisional thread binding failed")
                            }
                        },
                        Ok(Err(error)) if !error.is::<NativeFailure>() => {}
                        _ => host.retain_unresolved(open_owner),
                    }
                });
                return Err(mass_runtime::OpenUnknown.into());
            }
        };
        let handle = self
            .bind(opened, events, connection_id, open_owner, attachment_slot)
            .await?;
        if native_id
            .as_ref()
            .is_some_and(|expected| expected != &handle.thread)
        {
            self.rollback(&handle).await?;
            bail!("resume identity mismatch");
        }
        Ok(handle)
    }
    async fn bind(
        self: &Arc<Self>,
        opened: Value,
        events: mass_runtime::EventFactory,
        connection_id: String,
        open_owner: Arc<dyn Send + Sync>,
        attachment_slot: OwnedSemaphorePermit,
    ) -> Result<Arc<SessionHandle>> {
        if !self.online.load(Ordering::Acquire) || self.stopping.load(Ordering::Acquire) {
            self.retain_unresolved(open_owner);
            return Err(mass_runtime::OpenUnknown.into());
        }
        let Some(thread) = opened["thread"]["id"]
            .as_str()
            .filter(|id| !id.is_empty())
            .map(str::to_owned)
        else {
            self.retain_unresolved(open_owner);
            return Err(mass_runtime::OpenUnknown.into());
        };
        let mut routes = self.routes.lock().unwrap();
        if routes
            .get(&thread)
            .is_some_and(|old| old.online() || old.closing())
        {
            // A malformed duplicate result must not replace or unsubscribe a live or uncertain route.
            self.retain_unresolved(open_owner);
            return Err(mass_runtime::OpenUnknown.into());
        }
        let events = events(&thread);
        let delay = if self
            .observation
            .core_write_delay_thread
            .as_ref()
            .is_none_or(|cwd| opened["thread"]["cwd"] == cwd.to_string_lossy().as_ref())
        {
            self.observation.core_write_delay
        } else {
            Duration::ZERO
        };
        let (lane, receiver) = FactLane::new(
            self.context_capacity.clone(),
            self.recorder.clone(),
            connection_id.clone(),
            self.fact_lane_capacity,
        );
        let handle = Arc::new(SessionHandle {
            host: self.clone(),
            thread: thread.clone(),
            connection_id,
            events: lane,
            attachment_slot: StdMutex::new(Some(attachment_slot)),
            state: StdMutex::new(SessionState::default()),
            publication: Mutex::new(()),
            changed: Notify::new(),
            closed: AtomicBool::new(false),
            close_requested: AtomicBool::new(false),
            closing: AtomicBool::new(false),
            close_timed_out: AtomicBool::new(false),
            released: AtomicBool::new(false),
            provisional_owner: StdMutex::new(Some(open_owner)),
            context_capacity: Arc::new(Semaphore::new(MAX_CONTEXT)),
        });
        handle
            .events
            .start(receiver, events, Arc::downgrade(&handle), delay);
        routes.insert(thread, handle.clone());
        Ok(handle)
    }
    pub async fn rollback(&self, handle: &Arc<SessionHandle>) -> Result<()> {
        handle.close().await
    }
}
#[derive(Default)]
struct SessionState {
    submission: Option<Submission>,
    completions: VecDeque<CompletionEvidence>,
    tools: BTreeMap<String, ToolContext>,
    deferred: VecDeque<DeferredObservation>,
    cancel_generation: u64,
    cancellation: Option<Arc<Cancellation>>,
    // Native resolved and delayed policy publication settle independently.
    unresolved_native_approvals: usize,
    pending_policy_publications: usize,
}
struct CompletionEvidence {
    key: String,
    digest: String,
    _host: OwnedSemaphorePermit,
    _session: OwnedSemaphorePermit,
}
struct DeferredObservation {
    at: tokio::time::Instant,
    value: Value,
    _host: OwnedSemaphorePermit,
    _session: OwnedSemaphorePermit,
}
struct Cancellation {
    generation: u64,
    submission_id: Option<String>,
    result: watch::Sender<Option<std::result::Result<(), CommandError>>>,
    acknowledged: AtomicBool,
    failed: AtomicBool,
    native_settled: AtomicBool,
}
struct ToolContext {
    turn: String,
    payload: Value,
    expires: tokio::time::Instant,
    _permit: OwnedSemaphorePermit,
    _session_permit: OwnedSemaphorePermit,
}
struct PendingApproval {
    id: Value,
    owner: AttachmentOwner,
    turn: String,
    item: String,
    expires: tokio::time::Instant,
    response_written: AtomicBool,
    respond_at: tokio::time::Instant,
    _permit: OwnedSemaphorePermit,
    _session_permit: OwnedSemaphorePermit,
}
struct Submission {
    id: String,
    operation_id: String,
    rejected: bool,
    sent: Arc<AtomicBool>,
    accepted: bool,
    recorded: Option<String>,
    turn: Option<String>,
    terminal: bool,
    start_settled: bool,
    cancelled_unsent: bool,
    cancelled: bool,
    acceptance_done: bool,
    observer_done: bool,
}
impl Submission {
    fn settled(&self) -> bool {
        self.terminal && self.start_settled && self.acceptance_done && self.observer_done
    }
}
pub struct SessionHandle {
    host: Arc<Host>,
    thread: String,
    connection_id: String,
    events: FactLane,
    attachment_slot: StdMutex<Option<OwnedSemaphorePermit>>,
    state: StdMutex<SessionState>,
    publication: Mutex<()>,
    changed: Notify,
    closed: AtomicBool,
    close_requested: AtomicBool,
    closing: AtomicBool,
    close_timed_out: AtomicBool,
    released: AtomicBool,
    provisional_owner: StdMutex<Option<Arc<dyn Send + Sync>>>,
    context_capacity: Arc<Semaphore>,
}
impl SessionHandle {
    pub fn closing(&self) -> bool {
        (self.close_requested.load(Ordering::Acquire) || self.closed.load(Ordering::Acquire))
            && !(self.closed.load(Ordering::Acquire)
                && self.released.load(Ordering::Acquire)
                && self.events.is_drained())
    }
    /// A caller deadline never abandons the route or its real late close observation.
    pub async fn close(self: &Arc<Self>) -> Result<()> {
        let deadline = tokio::time::Instant::now() + self.host.close_timeout;
        if self.close_timed_out.load(Ordering::Acquire) && self.closing() {
            bail!("close_unknown: native thread release not confirmed");
        }
        if !self.close_requested.swap(true, Ordering::AcqRel) && self.online() {
            let handle = self.clone();
            tokio::spawn(async move {
                {
                    let _publication = handle.publication.lock().await;
                    handle.closing.store(true, Ordering::Release);
                }
                if let Err(error) = handle.clone().close_native().await {
                    tracing::warn!(%error, thread=%handle.thread, "thread close remains unknown");
                }
            });
        }
        let outcome = tokio::time::timeout_at(deadline, async {
            loop {
                let changed = self.changed.notified();
                if self.closed.load(Ordering::Acquire)
                    && self.released.load(Ordering::Acquire)
                    && self.events.is_drained()
                {
                    return Ok(());
                }
                changed.await;
            }
        })
        .await;
        match outcome {
            Ok(result) => result,
            Err(_) => {
                self.close_timed_out.store(true, Ordering::Release);
                bail!("close_unknown: native thread release not confirmed");
            }
        }
    }
    async fn close_native(self: Arc<Self>) -> Result<()> {
        // Closing admission does not detach facts or invalidate the interrupt itself.
        let _ = SessionHandle::cancel(&self).await;
        loop {
            let changed = self.changed.notified();
            {
                let state = self.state.lock().unwrap();
                if state.submission.as_ref().is_none_or(|s| s.settled())
                    && state.unresolved_native_approvals == 0
                    && state.pending_policy_publications == 0
                    && state
                        .cancellation
                        .as_ref()
                        .is_none_or(|c| c.native_settled.load(Ordering::Acquire))
                {
                    break;
                }
            }
            ensure!(
                self.host.online.load(Ordering::Acquire),
                "close_unknown: host disconnected"
            );
            changed.await;
        }
        // The route/released observer is registered before this request. Neither ACK
        // nor idle proves release; only notLoaded or the matching closed event does.
        let result = self
            .host
            .request_observed(
                "thread/unsubscribe",
                json!({"threadId":self.thread}),
                None,
                Some(self.owner()),
            )
            .await;
        if result.as_ref().is_ok_and(|v| v["status"] == "notLoaded") {
            self.released.store(true, Ordering::Release);
        }
        loop {
            let changed = self.changed.notified();
            if self.released.load(Ordering::Acquire) {
                break;
            }
            ensure!(
                self.host.online.load(Ordering::Acquire),
                "close_unknown: host disconnected"
            );
            changed.await;
        }
        self.events.seal(None);
        self.events.drain().await;
        self.host.routes.lock().unwrap().remove(&self.thread);
        self.attachment_slot.lock().unwrap().take();
        self.closed.store(true, Ordering::Release);
        self.changed.notify_waiters();
        Ok(())
    }
    pub fn native_session_id(&self) -> &str {
        &self.thread
    }
    pub fn commit_creation(&self) {
        self.provisional_owner.lock().unwrap().take();
    }
    fn owner(&self) -> AttachmentOwner {
        AttachmentOwner {
            host_generation: self.host.generation.clone(),
            cancel_generation: self.state.lock().unwrap().cancel_generation,
            thread: self.thread.clone(),
            connection_id: self.connection_id.clone(),
        }
    }
    pub fn online(&self) -> bool {
        !self.closed.load(Ordering::Acquire)
            && self.host.online.load(Ordering::Acquire)
            && !self.host.stopping.load(Ordering::Acquire)
    }
    pub fn runtime(&self) -> Value {
        self.host.identity.clone()
    }
    fn release_fence(&self, state: &mut SessionState) {
        if state.cancellation.as_ref().is_some_and(|c| {
            c.acknowledged.load(Ordering::Acquire) && !c.failed.load(Ordering::Acquire)
        }) && self.events.is_idle()
            && state.deferred.is_empty()
            && state.unresolved_native_approvals == 0
            && state.pending_policy_publications == 0
            && state.submission.as_ref().is_none_or(|s| s.settled())
        {
            state.tools.clear(); // No synthetic result for unfinished native tools.
            state.cancellation = None;
        }
    }
    pub async fn cancel(self: &Arc<Self>) -> std::result::Result<(), CommandError> {
        let publication = self.publication.lock().await;
        let round = {
            let mut state = self.state.lock().unwrap();
            if let Some(round) = &state.cancellation {
                // The original round remains the owner after observation is lost.
                round.clone()
            } else if !self.online() || !self.host.accepting.load(Ordering::Acquire) {
                return Err(CommandError::new(
                    CommandErrorKind::Offline,
                    "session is offline",
                ));
            } else if state.submission.as_ref().is_none_or(|s| s.settled())
                && state.unresolved_native_approvals == 0
                && state.pending_policy_publications == 0
            {
                return Ok(());
            } else {
                if let Some(s) = state.submission.as_mut() {
                    s.cancelled = true;
                }
                state.cancel_generation += 1;
                let (result, _) = watch::channel(None);
                let round = Arc::new(Cancellation {
                    generation: state.cancel_generation,
                    submission_id: state.submission.as_ref().map(|s| s.id.clone()),
                    result,
                    acknowledged: AtomicBool::new(false),
                    failed: AtomicBool::new(false),
                    native_settled: AtomicBool::new(false),
                });
                state.cancellation = Some(round.clone());
                let handle = self.clone();
                let task_round = round.clone();
                tokio::spawn(async move {
                    handle.interrupt(task_round).await;
                });
                round
            }
        };
        drop(publication);
        let mut result = round.result.subscribe();
        loop {
            if let Some(value) = result.borrow().clone() {
                return value;
            }
            if result.changed().await.is_err() {
                return Err(CommandError::new(
                    CommandErrorKind::CancelUnknown,
                    "cancel observer disconnected; outcome is unknown",
                ));
            }
        }
    }
    async fn interrupt_native(&self, round: &Cancellation) -> Result<()> {
        let turn = loop {
            let changed = self.changed.notified();
            {
                let state = self.state.lock().unwrap();
                if !self.online() || !self.host.accepting.load(Ordering::Acquire) {
                    bail!(NativeFailure::Disconnected);
                }
                ensure!(
                    state.cancel_generation == round.generation
                        && state.submission.as_ref().map(|s| &s.id) == round.submission_id.as_ref(),
                    "cancel target changed"
                );
                if let Some(s) = &state.submission {
                    if s.cancelled_unsent || s.rejected {
                        break None;
                    }
                    if let Some(turn) = &s.turn {
                        break Some(turn.clone());
                    }
                }
            }
            changed.await;
        };
        if let Some(turn) = turn {
            let (reply, response) = oneshot::channel();
            self.host.enqueue_command(
                HostCommand::Request {
                    method: "turn/interrupt",
                    params: json!({"threadId":self.thread,"turnId":turn}),
                    reply,
                    sent: None,
                    owner: Some(AttachmentOwner {
                        cancel_generation: round.generation,
                        ..self.owner()
                    }),
                    lane: self.connection_id.clone(),
                },
                true,
            )?;
            response.await.map_err(|_| NativeFailure::Disconnected)??;
        }
        Ok::<_, anyhow::Error>(())
    }
    async fn interrupt(self: Arc<Self>, round: Arc<Cancellation>) {
        // An HTTP deadline cannot abandon a late turn identity or an SDK request
        // still awaiting channel space. One round owns this task until settlement.
        let handle = self.clone();
        let native_round = round.clone();
        let mut work = tokio::spawn(async move {
            let result = handle.interrupt_native(&native_round).await;
            native_round.native_settled.store(true, Ordering::Release);
            handle.changed.notify_waiters();
            result
        });
        let value = match tokio::time::timeout(DEADLINE, &mut work).await {
            Ok(Ok(Ok(()))) => {
                round.acknowledged.store(true, Ordering::Release);
                Ok(())
            }
            _outcome => {
                round.failed.store(true, Ordering::Release);
                Err(CommandError::new(
                    CommandErrorKind::CancelUnknown,
                    "cancel outcome is unknown; original owner is settling it",
                ))
            }
        };
        round.result.send_replace(Some(value));
        self.release_fence(&mut self.state.lock().unwrap());
        self.changed.notify_waiters();
    }
    pub fn begin(
        self: &Arc<Self>,
        command: InputCommand,
    ) -> std::result::Result<
        oneshot::Receiver<std::result::Result<InputReceipt, CommandError>>,
        CommandError,
    > {
        let mut state = self.state.lock().unwrap();
        if self.close_requested.load(Ordering::Acquire) {
            return Err(CommandError::new(
                CommandErrorKind::Closing,
                "session attachment is closing",
            ));
        }
        if !self.online() || !self.host.accepting.load(Ordering::Acquire) {
            return Err(CommandError::new(
                CommandErrorKind::Offline,
                "session is offline",
            ));
        }
        if !self.events.admits_commands() {
            return Err(CommandError::new(
                CommandErrorKind::Backpressure,
                "core delivery lane is saturated",
            ));
        }
        self.release_fence(&mut state);
        if state.cancellation.is_some() {
            return Err(CommandError::new(
                CommandErrorKind::Cancelling,
                "cancellation is unsettled",
            ));
        }
        if state.unresolved_native_approvals != 0
            || state.pending_policy_publications != 0
            || !state.deferred.is_empty()
            || !self.events.is_idle()
            || state.submission.as_ref().is_some_and(|s| {
                !(s.settled()
                    && (s.cancelled || s.rejected || (s.accepted && s.recorded.is_some())))
            })
        {
            return Err(CommandError::new(
                CommandErrorKind::Busy,
                "session has active or unsettled input",
            ));
        }
        let id = uuid::Uuid::now_v7().to_string();
        state.submission = Some(Submission {
            id: id.clone(),
            operation_id: command.operation_id,
            rejected: false,
            sent: Arc::new(AtomicBool::new(false)),
            accepted: false,
            recorded: None,
            turn: None,
            terminal: false,
            start_settled: false,
            cancelled_unsent: false,
            cancelled: false,
            acceptance_done: false,
            observer_done: false,
        });
        let generation = state.cancel_generation;
        let (tx, rx) = oneshot::channel();
        let handle = self.clone();
        tokio::spawn(async move {
            let worker = handle.clone();
            let worker_id = id.clone();
            let mut task =
                tokio::spawn(
                    async move { worker.submit(worker_id, command.blocks, generation).await },
                );
            let result = match tokio::time::timeout(DEADLINE, &mut task).await {
                Ok(Ok(value)) => value,
                _ => Err(handle.acceptance_error(&id, None)),
            };
            let _ = tx.send(result);
            // Keep this submission's snapshot until its observer has produced
            // the receipt/error, including a deadline racing native completion.
            {
                let mut state = handle.state.lock().unwrap();
                state.submission.as_mut().unwrap().observer_done = true;
                handle.release_fence(&mut state);
            }
            handle.changed.notify_waiters();
        });
        Ok(rx)
    }
    async fn submit(
        &self,
        id: String,
        content: Vec<InputBlock>,
        generation: u64,
    ) -> std::result::Result<InputReceipt, CommandError> {
        let work = async {
            let sent = self
                .state
                .lock()
                .unwrap()
                .submission
                .as_ref()
                .unwrap()
                .sent
                .clone();
            let mut owner = self.owner();
            owner.cancel_generation = generation;
            let response = self
                .host
                .request_observed(
                    "turn/start",
                    json!({"threadId":self.thread,"clientUserMessageId":id,"input":content.into_iter().map(|InputBlock::Text { text }| json!({"type":"text","text":text,"text_elements":[]})).collect::<Vec<_>>()}),
                    Some(sent),
                    Some(owner),
                )
                .await;
            {
                let mut state = self.state.lock().unwrap();
                let submission = state.submission.as_mut().unwrap();
                submission.start_settled = true;
                if response
                    .as_ref()
                    .is_err_and(|error| error.is::<NativeRejected>())
                    && submission.turn.is_none()
                {
                    submission.rejected = true;
                    submission.terminal = true;
                }
                if response.as_ref().is_err_and(|e| {
                    matches!(
                        e.downcast_ref::<NativeFailure>(),
                        Some(NativeFailure::CancelledBeforeDispatch)
                    )
                }) {
                    submission.cancelled_unsent = true;
                    submission.terminal = true;
                }
            }
            self.changed.notify_waiters();
            let response = response?;
            let turn = response["turn"]["id"]
                .as_str()
                .context("missing native turn id")?
                .to_owned();
            {
                let mut state = self.state.lock().unwrap();
                let submission = state.submission.as_mut().unwrap();
                ensure!(
                    submission.turn.as_ref().is_none_or(|old| old == &turn),
                    "native turn identity mismatch"
                );
                submission.turn = Some(turn);
                submission.accepted = true;
            }
            self.changed.notify_waiters();
            loop {
                let notified = self.changed.notified();
                {
                    let state = self.state.lock().unwrap();
                    let submission = state.submission.as_ref().unwrap();
                    if let Some(message) = &submission.recorded {
                        return Ok(InputReceipt {
                            operation_id: submission.operation_id.clone(),
                            submission_id: id.clone(),
                            message_id: message.clone(),
                            acceptance: Acceptance::NativeRecorded,
                        });
                    }
                    if !self.online() || !self.host.accepting.load(Ordering::Acquire) {
                        bail!(NativeFailure::Disconnected);
                    }
                    ensure!(
                        !(submission.cancelled && submission.terminal),
                        "cancelled input has no native recorded echo"
                    );
                }
                notified.await;
            }
        };
        let result = match work.await {
            Ok(value) => Ok(value),
            Err(error) => Err(self.acceptance_error(&id, Some(&error))),
        };
        {
            let mut state = self.state.lock().unwrap();
            state.submission.as_mut().unwrap().acceptance_done = true;
            self.release_fence(&mut state);
        }
        self.changed.notify_waiters();
        result
    }
    fn acceptance_error(&self, id: &str, failure: Option<&anyhow::Error>) -> CommandError {
        let state = self.state.lock().unwrap();
        let s = state.submission.as_ref().unwrap();
        let kind = if s.cancelled_unsent {
            CommandErrorKind::Cancelled
        } else if failure.is_some_and(|error| error.is::<NativeRejected>()) {
            CommandErrorKind::Rejected
        } else {
            CommandErrorKind::AcceptanceUnknown
        };
        let mut error = CommandError::new(
            kind,
            match kind {
                CommandErrorKind::Cancelled => "input cancelled before dispatch",
                CommandErrorKind::Rejected => "Agent rejected the input",
                _ => "input acceptance is unknown; original owner is settling it",
            },
        );
        error.operation_id = Some(s.operation_id.clone());
        error.submission_id = Some(id.into());
        error.evidence = Some(AcceptanceEvidence {
            sent: s.sent.load(Ordering::Acquire),
            native_accepted: s.accepted,
            native_recorded: s.recorded.is_some(),
        });
        error
    }
    fn source(&self, params: &Value, evidence: &str) -> Value {
        let mut source = json!({"provider":"codex","nativeSessionId":self.thread,"threadId":self.thread,"connectionId":self.connection_id,"hostGeneration":self.host.generation,"completionEvidence":evidence});
        for field in ["turnId", "itemId", "completedAtMs"] {
            if let Some(value) = params.get(field) {
                source[field] = value.clone();
            }
        }
        source
    }
    fn defer(&self, value: Value, delay: Duration) -> Result<()> {
        let bytes = serde_json::to_vec(&value)?.len();
        ensure!(bytes <= MAX_CONTEXT, "deferred observation byte limit");
        let mut state = self.state.lock().unwrap();
        ensure!(
            state.deferred.len() < 64,
            "deferred observation count limit"
        );
        let host = self
            .host
            .context_capacity
            .clone()
            .try_acquire_many_owned(bytes as u32)?;
        let session = self
            .context_capacity
            .clone()
            .try_acquire_many_owned(bytes as u32)?;
        state.deferred.push_back(DeferredObservation {
            at: tokio::time::Instant::now() + delay,
            value,
            _host: host,
            _session: session,
        });
        Ok(())
    }
    async fn observe_deferred(&self) -> Result<()> {
        loop {
            let next = {
                let mut state = self.state.lock().unwrap();
                let ready = state
                    .deferred
                    .iter()
                    .position(|d| d.at <= tokio::time::Instant::now());
                ready.and_then(|i| state.deferred.remove(i))
            };
            let Some(next) = next else {
                return Ok(());
            };
            self.observe(&next.value).await?;
        }
    }
    async fn observe(&self, value: &Value) -> Result<()> {
        if value["method"] == "thread/closed" {
            self.released.store(true, Ordering::Release);
            if !self.close_requested.load(Ordering::Acquire) && !self.closed.load(Ordering::Acquire)
            {
                self.invalidate("native thread closed");
            }
            self.provisional_owner.lock().unwrap().take();
            self.changed.notify_waiters();
            return Ok(());
        }
        if self.closed.load(Ordering::Acquire) {
            return Ok(());
        }
        let params = &value["params"];
        for coordinate in ["turnId", "itemId"] {
            ensure!(
                params[coordinate].as_str().is_none_or(|s| s.len() <= 4096),
                "native coordinate exceeds 4 KiB; history incomplete"
            );
        }
        ensure!(
            params["item"]["id"]
                .as_str()
                .is_none_or(|s| s.len() <= 4096),
            "native item ID exceeds 4 KiB; history incomplete"
        );
        let method = value["method"].as_str().unwrap_or("");
        match method {
            "item/completed" => {
                let item = &params["item"];
                let id = item["id"].as_str().context("completed item missing id")?;
                let evidence = json!([
                    self.thread,
                    params["turnId"],
                    id,
                    item["type"],
                    params["completedAtMs"]
                ])
                .to_string();
                let digest = format!("{:x}", Sha256::digest(serde_json::to_vec(params)?));
                {
                    let mut state = self.state.lock().unwrap();
                    if let Some(previous) =
                        state.completions.iter().find(|entry| entry.key == evidence)
                    {
                        ensure!(
                            previous.digest == digest,
                            "conflicting native completion evidence"
                        );
                        return Ok(());
                    }
                    if state.completions.len() == 128 {
                        state.completions.pop_front();
                    }
                    let bytes = (evidence.len() + digest.len()) as u32;
                    let host = self
                        .host
                        .context_capacity
                        .clone()
                        .try_acquire_many_owned(bytes)
                        .context("host completion context limit")?;
                    let session = self
                        .context_capacity
                        .clone()
                        .try_acquire_many_owned(bytes)
                        .context("session completion context limit")?;
                    state.completions.push_back(CompletionEvidence {
                        key: evidence,
                        digest,
                        _host: host,
                        _session: session,
                    });
                }
                if item["type"] == "commandExecution" {
                    return self.complete_tool(params).await;
                }
                if item["type"] == "userMessage" {
                    let mut state = self.state.lock().unwrap();
                    let s = state.submission.as_mut().context("unsolicited user echo")?;
                    ensure!(item["clientId"] == s.id, "unmatched user echo");
                    let turn = params["turnId"].as_str().context("echo missing turn")?;
                    ensure!(
                        s.turn.as_ref().is_none_or(|old| old == turn),
                        "echo turn identity mismatch"
                    );
                    s.sent.store(true, Ordering::Release);
                    s.turn = Some(turn.into());
                }
                let (kind, content) = message_projection(item)?;
                let key = json!(["codex", self.thread, id]).to_string();
                let mut source = self.source(params, "item/completed");
                source["itemId"] = json!(id);
                if let Some(client) = item.get("clientId") {
                    source["clientId"] = client.clone();
                }
                let mut payload = json!({"content":content,"source":source,"nativeItem":item});
                if let Some(phase) = item.get("phase") {
                    payload["phase"] = phase.clone();
                }
                if kind == "user" {
                    self.state
                        .lock()
                        .unwrap()
                        .submission
                        .as_mut()
                        .unwrap()
                        .recorded = Some(mass_runtime::public_id(kind, &key));
                    self.changed.notify_waiters();
                }
                self.events
                    .native_message(kind, &key, payload, false)
                    .await?;
            }
            "item/agentMessage/delta"
            | "item/reasoning/summaryTextDelta"
            | "item/reasoning/textDelta" => {
                let id = params["itemId"]
                    .as_str()
                    .context("preview missing itemId")?;
                let kind = if method == "item/agentMessage/delta" {
                    "message"
                } else {
                    "thought"
                };
                let mut block = json!({"type":"text","text":params["delta"].as_str().context("preview missing delta")?});
                if kind == "thought" {
                    block["channel"] = json!(if method.contains("summary") {
                        "summary"
                    } else {
                        "content"
                    });
                    for index in ["summaryIndex", "contentIndex"] {
                        if let Some(v) = params.get(index) {
                            block["index"] = v.clone();
                        }
                    }
                }
                if !self.host.observation.drop_previews {
                    self.events
                        .native_message(
                            kind,
                            &json!(["codex", self.thread, id]).to_string(),
                            json!({"content":[block],"source":self.source(params,method)}),
                            true,
                        )
                        .await?;
                }
            }
            "turn/completed" => {
                let mut matched = false;
                {
                    let mut state = self.state.lock().unwrap();
                    if let Some(s) = state.submission.as_mut()
                        && s.turn
                            .as_ref()
                            .is_some_and(|id| params["turn"]["id"] == *id)
                    {
                        matched = true;
                    }
                }
                self.release_fence(&mut self.state.lock().unwrap());
                if matched {
                    // A failed turn is a distinct real terminal fact; do not synthesize any item.
                    self.events.native_state(
                        json!("idle"),
                        json!({"provider":"codex","turn":params["turn"],"threadId":self.thread} ),
                    ).await?;
                    {
                        let mut state = self.state.lock().unwrap();
                        state.submission.as_mut().unwrap().terminal = true;
                        self.release_fence(&mut state);
                    }
                    self.changed.notify_waiters();
                }
            }
            "turn/started" => {
                let turn = params["turn"]["id"]
                    .as_str()
                    .context("turn start missing id")?;
                {
                    let mut state = self.state.lock().unwrap();
                    let s = state.submission.as_mut().context("unsolicited turn")?;
                    ensure!(
                        s.turn.as_ref().is_none_or(|old| old == turn),
                        "turn start mismatch"
                    );
                    s.sent.store(true, Ordering::Release);
                    s.turn = Some(turn.into());
                }
                self.changed.notify_waiters();
                self.events
                    .native_state(json!("running"), self.source(params, method))
                    .await?;
            }
            "thread/status/changed" => { /* Thread status does not settle a pending turn/start. */ }
            "item/started" => {
                if params["item"]["type"] == "commandExecution" {
                    return self.start_tool(params).await;
                }
                ensure!(
                    matches!(
                        params["item"]["type"].as_str(),
                        Some("userMessage" | "agentMessage" | "reasoning")
                    ),
                    "unsupported_capability: native item type {}",
                    params["item"]["type"]
                );
            }
            _ => {}
        }
        Ok(())
    }
    fn command_valid(&self, owner: &AttachmentOwner, control: bool) -> bool {
        let state = self.state.lock().unwrap();
        self.online()
            && self.host.accepting.load(Ordering::Acquire)
            && owner.host_generation == self.host.generation
            && owner.connection_id == self.connection_id
            && owner.cancel_generation == state.cancel_generation
            && (control || !self.close_requested.load(Ordering::Acquire))
            && (control || !self.closing.load(Ordering::Acquire))
            && (control || state.cancellation.is_none())
    }
    fn policy_settled(&self) {
        let mut state = self.state.lock().unwrap();
        state.pending_policy_publications = state.pending_policy_publications.saturating_sub(1);
        self.release_fence(&mut state);
        self.changed.notify_waiters();
    }
    fn approval_settled(&self) {
        let mut state = self.state.lock().unwrap();
        state.unresolved_native_approvals = state.unresolved_native_approvals.saturating_sub(1);
        self.release_fence(&mut state);
        self.changed.notify_waiters();
    }
    fn approval_valid(&self, turn: &str, item: &str) -> bool {
        if !self.online() || !self.host.accepting.load(Ordering::Acquire) {
            return false;
        }
        let state = self.state.lock().unwrap();
        state.cancellation.is_none()
            && state
                .submission
                .as_ref()
                .is_some_and(|s| !s.terminal && s.turn.as_deref() == Some(turn))
            && state.tools.get(item).is_some_and(|call| call.turn == turn)
    }
    fn register_approval(
        &self,
        value: &Value,
        pending: &BTreeMap<String, Arc<PendingApproval>>,
    ) -> Result<PendingApproval> {
        let params = &value["params"];
        let id = &value["id"];
        ensure!(
            id.is_string() || id.is_i64() || id.is_u64(),
            "invalid native request ID"
        );
        let turn = params["turnId"].as_str().context("approval missing turn")?;
        let item = params["itemId"].as_str().context("approval missing item")?;
        ensure!(self.approval_valid(turn, item), "stale approval ownership");
        ensure!(
            params["availableDecisions"]
                .as_array()
                .is_some_and(|a| a.contains(&json!("accept"))),
            "once approval unavailable"
        );
        ensure!(
            !pending.contains_key(&id.to_string()),
            "duplicate native approval"
        );
        ensure!(
            self.state.lock().unwrap().pending_policy_publications < 128
                && pending
                    .values()
                    .filter(|a| a.owner.connection_id == self.connection_id)
                    .count()
                    < 128,
            "native approval count limit"
        );
        let bytes = serde_json::to_vec(value)?.len();
        ensure!(bytes <= MAX_CONTEXT, "native approval byte limit");
        let session_permit = self
            .context_capacity
            .clone()
            .try_acquire_many_owned(bytes as u32)
            .context("session context byte limit")?;
        let permit = self
            .host
            .context_capacity
            .clone()
            .try_acquire_many_owned(bytes as u32)
            .context("host context byte limit")?;
        {
            let mut state = self.state.lock().unwrap();
            state.unresolved_native_approvals += 1;
            state.pending_policy_publications += 1;
        }
        Ok(PendingApproval {
            id: id.clone(),
            owner: self.owner(),
            turn: turn.into(),
            item: item.into(),
            expires: tokio::time::Instant::now() + CONTEXT_TTL,
            response_written: AtomicBool::new(false),
            respond_at: tokio::time::Instant::now() + self.host.observation.approval_response_delay,
            _permit: permit,
            _session_permit: session_permit,
        })
    }
    fn execution_input(item: &Value) -> Result<Value> {
        ensure!(
            item["command"].is_string()
                && item["cwd"].is_string()
                && item["commandActions"].is_array(),
            "incomplete native execution projection"
        );
        Ok(
            json!({"command":item["command"],"cwd":item["cwd"],"commandActions":item["commandActions"]}),
        )
    }
    async fn start_tool(&self, params: &Value) -> Result<()> {
        let item = &params["item"];
        let id = item["id"].as_str().context("tool missing item")?;
        let turn = params["turnId"].as_str().context("tool missing turn")?;
        let key = json!(["codex", self.thread, id]).to_string();
        let mut source = self.source(params, "item/started");
        source["itemId"] = json!(id);
        if let Some(started) = params.get("startedAtMs") {
            source["startedAtMs"] = started.clone();
        }
        let payload = json!({"id":mass_runtime::public_id("tool",&key),"name":"commandExecution","title":item["command"],"kind":"execute","phase":"parameters_ready","inputRepresentation":"normalized_execution","rawInput":Self::execution_input(item)?,"nativeItem":item,"source":source});
        let bytes = serde_json::to_vec(&payload)?.len();
        {
            let mut state = self.state.lock().unwrap();
            let evidence = json!(["tool_start", self.thread, turn, id]).to_string();
            let digest = format!(
                "{:x}",
                Sha256::digest(serde_json::to_vec(&payload["rawInput"])?)
            );
            if let Some(previous) = state.completions.iter().find(|entry| entry.key == evidence) {
                ensure!(previous.digest == digest, "conflicting native tool input");
                return Ok(());
            }
            // Foreground termination does not erase ownership of late native facts.
            // Approval still independently requires a live, nonterminal work item.
            ensure!(
                state
                    .submission
                    .as_ref()
                    .is_some_and(|s| s.turn.as_deref() == Some(turn)),
                "unowned native tool turn"
            );
            ensure!(
                state.tools.len() < 128 && bytes <= MAX_CONTEXT,
                "native tool context limit"
            );
            let session_permit = self
                .context_capacity
                .clone()
                .try_acquire_many_owned(bytes as u32)
                .context("session context byte limit")?;
            let permit = self
                .host
                .context_capacity
                .clone()
                .try_acquire_many_owned(bytes as u32)
                .context("host context byte limit")?;
            // Keep the immutable call fingerprint after cancellation releases its
            // unfinished result context. Native startup metadata may arrive late.
            if state.completions.len() == 128 {
                state.completions.pop_front();
            }
            let evidence_bytes = (evidence.len() + digest.len()) as u32;
            state.completions.push_back(CompletionEvidence {
                key: evidence,
                digest,
                _host: self
                    .host
                    .context_capacity
                    .clone()
                    .try_acquire_many_owned(evidence_bytes)?,
                _session: self
                    .context_capacity
                    .clone()
                    .try_acquire_many_owned(evidence_bytes)?,
            });
            state.tools.insert(
                id.into(),
                ToolContext {
                    turn: turn.into(),
                    payload: payload.clone(),
                    expires: tokio::time::Instant::now() + CONTEXT_TTL,
                    _permit: permit,
                    _session_permit: session_permit,
                },
            );
        }
        self.events.native_tool(&key, payload, false).await
    }
    // Fixed 0.160.1 unified-exec HeadTailBuffer retains 512 KiB per side.
    // ASCII avoids byte inflation from native lossy UTF-8 decoding. A marker at
    // this exact boundary plus a full tail cannot be an untruncated buffer.
    fn output_metadata(item: &Value, output: &str) -> Value {
        let mut metadata = json!({"availability":"provided","completeness":"native_retained"});
        if item["source"] != "unifiedExecStartup"
            || item["status"] != "completed"
            || item["exitCode"] != 0
            || !output.is_ascii()
        {
            return metadata;
        }
        const HALF: usize = 512 * 1024;
        if let Some(rest) = output.get(HALF..).and_then(|s| s.strip_prefix("\n... "))
            && let Some((count, tail)) = rest.split_once(" bytes omitted ...\n")
            && !count.is_empty()
            && count.bytes().all(|b| b.is_ascii_digit())
            && let Ok(omitted) = count.parse::<u64>()
            && omitted > 0
            && tail.len() == HALF
        {
            metadata["completeness"] = json!("truncated");
            metadata["truncationEvidence"] = json!({"kind":"codex_0_160_1_head_tail_marker","omittedBytes":omitted,"retainedBytes":HALF*2});
        }
        metadata
    }
    async fn complete_tool(&self, params: &Value) -> Result<()> {
        let item = &params["item"];
        let id = item["id"].as_str().context("tool result missing item")?;
        let native_status = item["status"]
            .as_str()
            .context("tool result missing status")?;
        let status = match native_status {
            "completed" => "completed",
            "failed" => "failed",
            "declined" => "declined",
            _ => "unknown",
        };
        let context = self.state.lock().unwrap().tools.remove(id);
        if let Some(call) = &context {
            ensure!(params["turnId"] == call.turn, "tool result turn mismatch");
        }
        let mut source = self.source(params, "item/completed");
        source["itemId"] = json!(id);
        let mut payload = json!({"name":"commandExecution","kind":"execute","nativeStatus":native_status,"status":status,"nativeResult":item,"callContext":context.as_ref().map_or(json!("missing"),|c|c.payload.clone()),"inputRepresentation":"normalized_execution","rawInput":Self::execution_input(item)?,"output":{"availability":"unavailable","completeness":"unknown"},"source":source});
        for field in ["exitCode", "durationMs"] {
            if let Some(value) = item.get(field).filter(|v| !v.is_null()) {
                payload[field] = value.clone();
            }
        }
        if let Some(output) = item.get("aggregatedOutput").filter(|v| !v.is_null()) {
            ensure!(output.is_string(), "unsupported native command output");
            payload["rawOutput"] = output.clone();
            payload["content"] = json!([{"type":"text","text":output}]);
            payload["output"] =
                Self::output_metadata(item, output.as_str().expect("validated output string"));
        }
        self.events
            .native_tool(
                &json!(["codex", self.thread, id]).to_string(),
                payload,
                true,
            )
            .await
    }
    fn invalidate(&self, reason: &str) {
        self.closed.store(true, Ordering::Release);
        {
            let mut state = self.state.lock().unwrap();
            state.tools.clear();
            state.completions.clear();
            state.deferred.clear();
        }
        self.events.seal(Some(reason));
        self.changed.notify_waiters();
    }
    pub async fn disconnect(&self, reason: &str) {
        self.invalidate(reason);
    }
}

impl SessionControl for SessionHandle {
    fn begin(
        self: Arc<Self>,
        command: InputCommand,
    ) -> std::result::Result<
        oneshot::Receiver<std::result::Result<InputReceipt, CommandError>>,
        CommandError,
    > {
        SessionHandle::begin(&self, command)
    }
    fn cancel(self: Arc<Self>) -> RuntimeFuture<'static, std::result::Result<(), CommandError>> {
        Box::pin(async move {
            if self.close_requested.load(Ordering::Acquire) {
                return Err(CommandError::new(
                    CommandErrorKind::Closing,
                    "session attachment is closing",
                ));
            }
            SessionHandle::cancel(&self).await
        })
    }
}

/// Codex-only projection. Native objects and validation rules never cross Runtime.
fn public_payload(event_type: &str, payload: &Value, id: &str) -> Value {
    let mut public = json!({});
    if matches!(event_type, "agent.tool_call" | "agent.tool_result") {
        public["id"] = json!(id);
        public["name"] = json!("shell");
        public["kind"] = json!("execute");
        public["inputRepresentation"] = json!("execution");
        public["rawInput"] =
            json!({"command":payload["rawInput"]["command"],"cwd":payload["rawInput"]["cwd"]});
        for field in [
            "title",
            "phase",
            "status",
            "exitCode",
            "durationMs",
            "rawOutput",
            "content",
            "output",
        ] {
            if let Some(value) = payload.get(field) {
                public[field] = value.clone();
            }
        }
        if public["output"]["completeness"] == "native_retained" {
            public["output"]["completeness"] = json!("retained");
        }
        if let Some(output) = public.get_mut("output").and_then(Value::as_object_mut)
            && let Some(mut truncation) = output.remove("truncationEvidence")
        {
            truncation.as_object_mut().unwrap().remove("kind");
            output.insert("truncation".into(), truncation);
        }
        if event_type == "agent.tool_result" {
            public["callContext"] = if payload["callContext"].is_object() {
                public_payload("agent.tool_call", &payload["callContext"], id)
            } else {
                json!("missing")
            };
        }
    } else {
        public["messageId"] = json!(id);
        public["content"] = payload["content"].as_array().map_or(Value::Null, |blocks| {
            json!(
                blocks
                    .iter()
                    .map(|block| {
                        let mut text = json!({"type":"text","text":block["text"]});
                        for field in ["channel", "index"] {
                            if let Some(value) = block.get(field) {
                                text[field] = value.clone();
                            }
                        }
                        text
                    })
                    .collect::<Vec<_>>()
            )
        });
        if let Some(phase) = payload.get("phase").filter(|phase| !phase.is_null()) {
            public["phase"] = phase.clone();
        }
    }
    public
}

fn normalize_fact(
    event_type: &str,
    kind: &str,
    key: &str,
    payload: Value,
    preview: bool,
) -> Result<mass_runtime::RuntimeFact> {
    let id = mass_runtime::public_id(kind, key);
    let public = public_payload(event_type, &payload, &id);
    Ok(mass_runtime::RuntimeFact {
        event_type: event_type.into(),
        item_id: Some(id),
        payload: public,
        evidence: if preview {
            Value::Null
        } else {
            json!({"adapter":"codex","version":1,"payload":payload})
        },
        transient: preview,
    })
}

/// Validate private native evidence and its exact public projection before recovery.
pub fn validate_history_record(session_id: &str, event: &Value, evidence: &Value) -> Result<()> {
    ensure!(
        evidence["adapter"] == "codex" && evidence["version"] == 1,
        "unsupported Codex evidence"
    );
    let payload = &evidence["payload"];
    let source = &payload["source"];
    ensure!(
        source["provider"] == "codex"
            && source["nativeSessionId"] == session_id
            && source["threadId"] == session_id,
        "history native provider/session identity mismatch"
    );
    for field in ["turnId", "itemId"] {
        ensure!(
            source[field].as_str().is_some_and(|s| !s.is_empty()),
            "history missing native identity"
        );
    }
    for field in ["connectionId", "hostGeneration"] {
        uuid::Uuid::parse_str(
            source[field]
                .as_str()
                .context("missing source generation")?,
        )?;
    }
    let event_type = event["type"].as_str().context("missing event type")?;
    validate_history_payload(event_type, payload)?;
    let kind = match event_type {
        "user.message" => "user",
        "agent.message" => "message",
        "agent.thinking" => "thought",
        "agent.tool_call" | "agent.tool_result" => "tool",
        _ => bail!("unsupported history event"),
    };
    let key = json!(["codex", session_id, source["itemId"]]).to_string();
    let id = mass_runtime::public_id(kind, &key);
    ensure!(
        event["itemId"] == id && event["payload"] == public_payload(event_type, payload, &id),
        "history public projection mismatch"
    );
    if kind != "tool" {
        ensure!(
            payload["content"].as_array().is_some_and(|blocks| blocks
                .iter()
                .all(|b| b["type"] == "text" && b["text"].is_string())),
            "invalid text blocks"
        );
    }
    Ok(())
}

/// Revalidate saved provider payloads without opening a runtime or importing history.
pub fn validate_history_payload(event_type: &str, payload: &Value) -> Result<()> {
    let result = event_type == "agent.tool_result";
    ensure!(
        payload["source"]["completionEvidence"]
            == if event_type == "agent.tool_call" {
                "item/started"
            } else {
                "item/completed"
            },
        "history payload completion evidence mismatch"
    );
    let item = &payload[if result { "nativeResult" } else { "nativeItem" }];
    let expected_type = match event_type {
        "user.message" => "userMessage",
        "agent.message" => "agentMessage",
        "agent.thinking" => "reasoning",
        "agent.tool_call" | "agent.tool_result" => "commandExecution",
        _ => bail!("unsupported history event"),
    };
    ensure!(
        item["id"] == payload["source"]["itemId"] && item["type"] == expected_type,
        "history native payload coordinate/type mismatch"
    );
    let _: codex_app_server_protocol::ThreadItem =
        serde_json::from_value(item.clone()).context("invalid persisted native item")?;
    if !matches!(event_type, "agent.tool_call" | "agent.tool_result") {
        let (_, content) = message_projection(item)?;
        ensure!(
            payload["content"] == content,
            "history message projection mismatch"
        );
        if event_type == "agent.message" {
            ensure!(
                payload.get("phase") == item.get("phase"),
                "history agent phase mismatch"
            );
        }
        if event_type == "user.message" {
            ensure!(
                payload["source"]["clientId"] == item["clientId"],
                "history user client identity mismatch"
            );
        }
        return Ok(());
    }
    ensure!(
        payload["name"] == "commandExecution"
            && payload["kind"] == "execute"
            && payload["inputRepresentation"] == "normalized_execution"
            && payload["rawInput"] == SessionHandle::execution_input(item)?,
        "history command projection mismatch"
    );
    if !result {
        ensure!(
            payload["title"] == item["command"] && payload["phase"] == "parameters_ready",
            "history command call mismatch"
        );
        ensure!(
            payload.get("content").is_none() && payload.get("rawOutput").is_none(),
            "history command call contains output"
        );
        return Ok(());
    }
    let status = match item["status"].as_str() {
        Some("completed") => "completed",
        Some("failed") => "failed",
        Some("declined") => "declined",
        _ => "unknown",
    };
    ensure!(
        payload["nativeStatus"] == item["status"] && payload["status"] == status,
        "history command status mismatch"
    );
    for field in ["exitCode", "durationMs"] {
        ensure!(
            payload.get(field).filter(|v| !v.is_null()) == item.get(field).filter(|v| !v.is_null()),
            "history command result field mismatch"
        );
    }
    if let Some(output) = item["aggregatedOutput"].as_str() {
        ensure!(
            payload["rawOutput"] == output
                && payload["content"] == json!([{"type":"text","text":output}])
                && payload["output"] == SessionHandle::output_metadata(item, output),
            "history command output mismatch"
        );
    } else {
        ensure!(
            payload.get("rawOutput").is_none()
                && payload.get("content").is_none()
                && payload["output"]
                    == json!({"availability":"unavailable","completeness":"unknown"}),
            "history unavailable command output mismatch"
        );
    }
    let call = &payload["callContext"];
    if call != "missing" {
        validate_history_payload("agent.tool_call", call)?;
        for field in [
            "provider",
            "nativeSessionId",
            "threadId",
            "turnId",
            "itemId",
            "connectionId",
            "hostGeneration",
        ] {
            ensure!(
                call["source"][field] == payload["source"][field],
                "history call context source mismatch"
            );
        }
        let key = json!([
            "codex",
            payload["source"]["nativeSessionId"],
            payload["source"]["itemId"]
        ])
        .to_string();
        ensure!(
            call["id"] == mass_runtime::public_id("tool", &key),
            "history call context identity mismatch"
        );
    }
    Ok(())
}

fn message_projection(item: &Value) -> Result<(&'static str, Value)> {
    match item["type"].as_str() {
        Some("userMessage") => {
            ensure!(
                item["content"].as_array().is_some_and(|blocks| blocks
                    .iter()
                    .all(|block| block["type"] == "text" && block["text"].is_string())),
                "unsupported user echo content"
            );
            Ok(("user", item["content"].clone()))
        }
        Some("agentMessage") => Ok((
            "message",
            json!([{"type":"text","text":item["text"].as_str().context("completed agent text missing")?}]),
        )),
        Some("reasoning") => {
            let mut blocks = Vec::new();
            for channel in ["summary", "content"] {
                for (index, text) in item[channel]
                    .as_array()
                    .context("reasoning channel missing")?
                    .iter()
                    .enumerate()
                {
                    ensure!(text.is_string(), "unsupported reasoning block");
                    blocks.push(json!({"type":"text","channel":channel,"index":index,"text":text}));
                }
            }
            Ok(("thought", json!(blocks)))
        }
        _ => bail!("unsupported_capability: native item type {}", item["type"]),
    }
}

impl mass_runtime::AttachmentControl for SessionHandle {
    fn online(&self) -> bool {
        SessionHandle::online(self)
    }
    fn closing(&self) -> bool {
        SessionHandle::closing(self)
    }
    fn close(self: Arc<Self>) -> RuntimeFuture<'static, Result<()>> {
        Box::pin(async move { SessionHandle::close(&self).await })
    }
    fn commit(&self) {
        self.commit_creation();
    }
}
impl mass_runtime::Runtime for ProviderRuntime {
    fn runtime_type(&self) -> &str {
        if self.remote.is_some() {
            "codex-remote"
        } else {
            "codex"
        }
    }
    fn supports_recovery(&self) -> bool {
        self.remote.is_none()
    }
    fn validate_cwd(&self, cwd: &std::path::Path) -> Result<()> {
        ensure!(
            cwd.is_absolute()
                && !cwd.as_os_str().as_encoded_bytes().contains(&0)
                && (self.remote.is_some() || cwd.is_dir()),
            "cwd must be an absolute directory in the Runtime filesystem"
        );
        Ok(())
    }
    fn fingerprint(&self) -> Result<String> {
        if let Some(remote) = &self.remote {
            return remote.fingerprint();
        }
        // Use the file-registration encoding for both composition paths. Validation
        // remains the file factory's job; low-level constructors also support fault tests.
        let config = RegistrationConfig {
            command: self.config.executable.clone(),
            sha256: self.config.sha256.to_ascii_lowercase(),
            args: self.launch_args.clone(),
            env: self.config.env.clone(),
            secret_refs: self.secret_refs.clone(),
            close_timeout_ms: self.config.close_timeout_ms,
            fact_lane_capacity: self.config.fact_lane_capacity,
            sdk_event_bytes: self.config.sdk_event_bytes,
        };
        Ok(format!(
            "{:x}",
            Sha256::digest(serde_json::to_vec(&config)?)
        ))
    }
    fn validate_recovery(&self, id: &str, binding: &Value, cwd: &std::path::Path) -> Result<()> {
        if self.remote.is_some() {
            return Err(remote::recovery_unsupported());
        }
        uuid::Uuid::parse_str(id)?;
        let runtime = &binding["runtime"];
        ensure!(
            binding["adapter"] == "codex"
                && binding["version"] == 2
                && binding["sessionId"] == id
                && runtime["provider"] == "codex"
                && runtime["connectorVersion"] == "2"
                && runtime["version"] == VERSION
                && runtime["transport"] == "app_server_unix_websocket"
                && runtime["executable"] == self.config.executable.to_string_lossy().as_ref()
                && runtime["sha256"] == self.config.sha256,
            "unsupported recovery binding"
        );
        let environment = self.environment()?;
        let global_scope = recovery::scope(&environment);
        let scope = recovery::session_scope(&environment, cwd, global_scope.as_deref());
        if scope.is_none() || binding["scope"].as_str() != scope.as_deref() {
            return Err(mass_runtime::LifecycleError {
                kind: "recovery_unavailable",
                message: "Recovery account or data ownership is unavailable or changed",
            }
            .into());
        }
        Ok(())
    }
    fn status(&self) -> RuntimeFuture<'_, Value> {
        Box::pin(ProviderRuntime::status(self))
    }
    fn close_admission(&self) -> RuntimeFuture<'_, ()> {
        Box::pin(ProviderRuntime::close_admission(self))
    }
    fn stop(&self) -> RuntimeFuture<'_, Result<()>> {
        Box::pin(async move {
            ProviderRuntime::stop(self).await;
            ensure!(
                self.status().await["phase"] == "stopped",
                "runtime resource release is unknown"
            );
            Ok(())
        })
    }
    fn validate_history_record(&self) -> fn(&str, &Value, &Value) -> Result<()> {
        validate_history_record
    }
    fn open(
        self: Arc<Self>,
        request: mass_runtime::OpenRequest,
    ) -> RuntimeFuture<'static, Result<mass_runtime::OpenedAttachment>> {
        Box::pin(async move {
            mass_runtime::Runtime::validate_cwd(self.as_ref(), &request.cwd)?;
            if let Some((id, binding)) = &request.recovery {
                mass_runtime::Runtime::validate_recovery(self.as_ref(), id, binding, &request.cwd)?;
            }
            let activating = self.clone();
            // An observation deadline does not cancel activation ownership or its phase settlement.
            let initialization = tokio::spawn(async move { activating.start().await });
            let host = match tokio::time::timeout_at(request.deadline, initialization).await {
                Ok(Ok(Ok(host))) => host,
                _ => {
                    return Err(mass_runtime::LifecycleError {
                        kind: "runtime_unavailable",
                        message: "Runtime activation failed or is still settling",
                    }
                    .into());
                }
            };
            if let Some((id, binding)) = &request.recovery {
                mass_runtime::Runtime::validate_recovery(self.as_ref(), id, binding, &request.cwd)?;
                ensure!(
                    binding["scope"].as_str()
                        == recovery::session_scope(
                            &self.environment()?,
                            &request.cwd,
                            host.recovery_scope.as_deref()
                        )
                        .as_deref(),
                    "active host recovery ownership mismatch"
                );
            }
            let environment = self.environment()?;
            let current_scope = if self.remote.is_none() {
                recovery::scope(&environment)
            } else {
                None
            };
            let scope = if self.remote.is_none() {
                recovery::session_scope_for_host(
                    &environment,
                    &request.cwd,
                    host.recovery_scope.as_deref(),
                    current_scope.as_deref(),
                )
            } else {
                None
            };
            if let Some((_, binding)) = &request.recovery
                && scope.as_deref() != binding["scope"].as_str()
            {
                return Err(mass_runtime::LifecycleError {
                    kind: "recovery_unavailable",
                    message: "Recovery account or data ownership is unavailable or changed",
                }
                .into());
            }
            let expected = request.recovery.map(|(id, _)| id);
            let handle = host
                .open_session(
                    request.cwd,
                    expected.clone(),
                    request.events,
                    request.connection_id,
                    request.deadline,
                    request.owner,
                )
                .await
                .map_err(|error| {
                    if error.is::<mass_runtime::OpenUnknown>() {
                        error
                    } else {
                        mass_runtime::LifecycleError {
                            kind: "open_failed",
                            message: "Agent session open failed",
                        }
                        .into()
                    }
                })?;
            if expected.as_ref().is_some_and(|id| *id != handle.thread) {
                host.rollback(&handle).await?;
                bail!("resume identity mismatch");
            }
            Ok(mass_runtime::OpenedAttachment {
                session_id: handle.thread.clone(),
                recovery: json!({"adapter":"codex","version":2,"sessionId":handle.thread,"runtime":handle.runtime(),"scope":scope}),
                control: handle,
            })
        })
    }
}

#[cfg(test)]
mod settlement_tests;
