//! Construction-time extensions. The event core never calls extension code.
use futures_util::FutureExt;
use mass_runtime::SessionCommands;
use serde_json::Value;
use std::{
    collections::BTreeMap,
    future::Future,
    panic::AssertUnwindSafe,
    pin::Pin,
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio::{
    sync::{mpsc, oneshot},
    task::JoinHandle,
};

pub type HandlerFuture<'a> = Pin<Box<dyn Future<Output = anyhow::Result<()>> + Send + 'a>>;

/// Shared event with no mutable access, including when there is only one owner.
#[derive(Clone, Debug)]
pub struct ImmutableEvent(Arc<Value>);
impl ImmutableEvent {
    pub(crate) fn new(value: Value) -> Self {
        Self(Arc::new(value))
    }
    pub fn same_object(&self, other: &Self) -> bool {
        Arc::ptr_eq(&self.0, &other.0)
    }
    pub fn value(&self) -> &Value {
        &self.0
    }
}

#[derive(Clone, Debug)]
pub struct SessionIdentity {
    pub session_name: String,
    pub session_id: String,
    /// Internal stable instance scope; the Agent ID is not globally unique.
    pub runtime_instance_id: String,
    /// Identifies this connection, not the cancellation admission generation.
    pub connection_id: uuid::Uuid,
}

type SessionKey = (String, String);
impl SessionIdentity {
    fn key(&self) -> SessionKey {
        (self.runtime_instance_id.clone(), self.session_id.clone())
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DeliveryKind {
    Transient,
    Committed,
}

#[derive(Clone)]
pub struct HandlerContext {
    pub session: SessionIdentity,
    pub delivery: DeliveryKind,
    /// Bound to this connection. Never redirects work to a replacement runtime.
    pub commands: SessionCommands,
}

#[derive(Clone)]
pub enum Selection {
    All,
    Committed,
    Types(Vec<String>),
}
impl Selection {
    fn accepts(&self, event: &ImmutableEvent) -> bool {
        match self {
            Self::All => true,
            Self::Committed => event.value().get("seq").is_some(),
            Self::Types(types) => event.value()["type"]
                .as_str()
                .is_some_and(|kind| types.iter().any(|t| t == kind)),
        }
    }
}

/// One instance per session and registration; calls are serial and outside core locks.
pub trait EventHandler: Send {
    fn handle<'a>(
        &'a mut self,
        context: &'a HandlerContext,
        event: &'a ImmutableEvent,
    ) -> HandlerFuture<'a>;
    fn close(&mut self) -> HandlerFuture<'_> {
        Box::pin(async { Ok(()) })
    }
}

/// Only the core can construct this wrapper, after a successful Journal append.
#[derive(Clone)]
pub struct CommittedEvent(ImmutableEvent);
impl CommittedEvent {
    pub fn event(&self) -> &ImmutableEvent {
        &self.0
    }
}

/// Notifications are bounded, best-effort wakes. Reliable consumers must catch up
/// from the Journal using their own persistent progress, including after overflow.
pub trait CommittedEventConsumer: Send {
    fn consume<'a>(
        &'a mut self,
        context: &'a HandlerContext,
        event: &'a CommittedEvent,
    ) -> HandlerFuture<'a>;
    fn close(&mut self) -> HandlerFuture<'_> {
        Box::pin(async { Ok(()) })
    }
}
struct CommittedHandler(Box<dyn CommittedEventConsumer>);
impl EventHandler for CommittedHandler {
    fn handle<'a>(
        &'a mut self,
        context: &'a HandlerContext,
        event: &'a ImmutableEvent,
    ) -> HandlerFuture<'a> {
        Box::pin(async move {
            self.0
                .consume(context, &CommittedEvent(event.clone()))
                .await
        })
    }
    fn close(&mut self) -> HandlerFuture<'_> {
        self.0.close()
    }
}

type Factory = dyn Fn(&SessionIdentity) -> Box<dyn EventHandler> + Send + Sync;

pub struct HandlerRegistration {
    name: String,
    capacity: std::num::NonZeroUsize,
    selection: Selection,
    factory: Arc<Factory>,
}
impl HandlerRegistration {
    pub fn new(
        name: impl Into<String>,
        capacity: std::num::NonZeroUsize,
        selection: Selection,
        factory: impl Fn(&SessionIdentity) -> Box<dyn EventHandler> + Send + Sync + 'static,
    ) -> Self {
        Self {
            name: name.into(),
            capacity,
            selection,
            factory: Arc::new(factory),
        }
    }
    pub fn committed(
        name: impl Into<String>,
        capacity: std::num::NonZeroUsize,
        factory: impl Fn(&SessionIdentity) -> Box<dyn CommittedEventConsumer> + Send + Sync + 'static,
    ) -> Self {
        Self::new(name, capacity, Selection::Committed, move |identity| {
            Box::new(CommittedHandler(factory(identity)))
        })
    }
}

#[derive(Default)]
struct Workers {
    sessions: BTreeMap<SessionKey, Dispatcher>,
    tasks: Vec<WorkerTask>,
}
struct WorkerTask {
    session_key: SessionKey,
    cancel: Option<oneshot::Sender<()>>,
    task: JoinHandle<()>,
}

/// Unsuccessful construction must not retain service-lifetime consumers.
pub struct PendingHandlers<'a> {
    owner: &'a HandlerTasks,
    session_key: SessionKey,
    accepted: bool,
}
impl PendingHandlers<'_> {
    pub fn accept(mut self) {
        self.accepted = true;
    }
}
impl Drop for PendingHandlers<'_> {
    fn drop(&mut self) {
        if !self.accepted {
            self.owner.discard(&self.session_key);
        }
    }
}

struct Delivery {
    context: HandlerContext,
    event: ImmutableEvent,
}

pub struct HandlerTasks {
    registrations: Vec<HandlerRegistration>,
    tasks: Mutex<Workers>,
    shutdown_timeout: Duration,
}
impl HandlerTasks {
    pub fn new(registrations: Vec<HandlerRegistration>, shutdown_timeout: Duration) -> Self {
        Self {
            registrations,
            tasks: Mutex::new(Workers::default()),
            shutdown_timeout,
        }
    }
    pub fn pending(&self, instance_id: &str, session_id: &str) -> PendingHandlers<'_> {
        PendingHandlers {
            owner: self,
            session_key: (instance_id.into(), session_id.into()),
            accepted: false,
        }
    }
    fn discard(&self, session_key: &SessionKey) {
        let mut workers = self.tasks.lock().unwrap();
        if let Some(dispatcher) = workers.sessions.remove(session_key) {
            dispatcher.close();
        }
        for worker in &mut workers.tasks {
            if &worker.session_key == session_key
                && let Some(cancel) = worker.cancel.take()
            {
                let _ = cancel.send(());
            }
        }
    }
    pub(crate) fn open(&self, context: HandlerContext) -> Dispatcher {
        let mut lanes = Vec::new();
        let mut tasks = self.tasks.lock().unwrap();
        tasks.tasks.retain(|worker| !worker.task.is_finished());
        if let Some(dispatcher) = tasks.sessions.get(&context.session.key()) {
            return dispatcher.with_context(context);
        }
        for registration in &self.registrations {
            let (sender, mut receiver) = mpsc::channel::<Delivery>(registration.capacity.get());
            let factory = registration.factory.clone();
            let context = context.clone();
            let name = registration.name.clone();
            let (cancel, mut cancelled) = oneshot::channel();
            let close_timeout = self.shutdown_timeout;
            tasks.tasks.push(WorkerTask {
                session_key: context.session.key(),
                cancel: Some(cancel),
                task: tokio::spawn(async move {
                    // Catch factory, synchronous callback and asynchronous callback panics.
                    let result = AssertUnwindSafe(async {
                        let mut handler = factory(&context.session);
                        loop {
                            let delivery = tokio::select! {
                                biased;
                                _ = &mut cancelled => break,
                                delivery = receiver.recv() => delivery,
                            };
                            let Some(Delivery { context, event }) = delivery else { break; };
                            let result = tokio::select! {
                                biased;
                                _ = &mut cancelled => break,
                                result = AssertUnwindSafe(async { handler.handle(&context, &event).await }).catch_unwind() => result,
                            };
                            if !matches!(result, Ok(Ok(()))) {
                                tracing::warn!(handler = %name, session = %context.session.session_name, ?result, "event handler disabled after failure");
                                break;
                            }
                        }
                        // Failed construction also has a bound, even without service shutdown.
                        tokio::time::timeout(close_timeout, async { handler.close().await }).await??;
                        Ok::<_, anyhow::Error>(())
                    }).catch_unwind().await;
                    if !matches!(result, Ok(Ok(()))) {
                        tracing::warn!(handler = %name, ?result, "event handler close or construction failed");
                    }
                }),
            });
            lanes.push(Lane {
                name: registration.name.clone(),
                selection: registration.selection.clone(),
                sender: Arc::new(Mutex::new(Some(sender))),
            });
        }
        let dispatcher = Dispatcher {
            lanes,
            context: Some(context.clone()),
        };
        tasks.sessions.insert(
            context.session.key(),
            Dispatcher {
                lanes: dispatcher.lanes.clone(),
                context: None,
            },
        );
        dispatcher
    }
    pub async fn shutdown(&self) {
        let tasks = {
            let mut workers = self.tasks.lock().unwrap();
            for dispatcher in workers.sessions.values() {
                dispatcher.close();
            }
            workers.sessions.clear();
            std::mem::take(&mut workers.tasks)
        };
        let deadline = tokio::time::Instant::now() + self.shutdown_timeout;
        for WorkerTask {
            mut task, cancel, ..
        } in tasks
        {
            // Keep rollback signals alive during graceful queue draining.
            let _cancel = cancel;
            if tokio::time::timeout_at(deadline, &mut task).await.is_err() {
                tracing::warn!("event handler shutdown timed out; aborting consumer");
                task.abort();
                let _ = task.await;
            }
        }
    }
}
impl Drop for HandlerTasks {
    fn drop(&mut self) {
        for worker in self.tasks.get_mut().unwrap().tasks.drain(..) {
            worker.task.abort();
        }
    }
}
#[derive(Clone)]
struct Lane {
    name: String,
    selection: Selection,
    sender: Arc<Mutex<Option<mpsc::Sender<Delivery>>>>,
}
#[derive(Clone, Default)]
pub(crate) struct Dispatcher {
    lanes: Vec<Lane>,
    context: Option<HandlerContext>,
}
impl Dispatcher {
    fn with_context(&self, context: HandlerContext) -> Self {
        Self {
            lanes: self.lanes.clone(),
            context: Some(context),
        }
    }
    fn close(&self) {
        for lane in &self.lanes {
            lane.sender.lock().unwrap().take();
        }
    }
    pub fn publish(&mut self, event: &ImmutableEvent) {
        for lane in &mut self.lanes {
            if !lane.selection.accepts(event) {
                continue;
            }
            let mut sender = lane.sender.lock().unwrap();
            let Some(channel) = sender.as_ref() else {
                continue;
            };
            let mut context = self
                .context
                .as_ref()
                .expect("active dispatcher context")
                .clone();
            context.delivery = if event.value().get("seq").is_some() {
                DeliveryKind::Committed
            } else {
                DeliveryKind::Transient
            };
            if let Err(error) = channel.try_send(Delivery {
                event: event.clone(),
                context,
            }) {
                tracing::warn!(handler = %lane.name, session = %event.value()["sessionName"], %error, "event handler disabled; bounded delivery unavailable");
                // Drain accepted events and close; never silently skip and resume.
                *sender = None;
            }
        }
    }
}
