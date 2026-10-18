//! Bounded normalized-fact delivery. The official reader never waits for Journal I/O.
use super::*;
use std::sync::Weak;

// A queue permit includes the currently executing fact. Context and queued payloads
// share the host budget; it does not cover allocations inside the official SDK.
pub(super) struct Delivery {
    fact: Fact,
    _count: OwnedSemaphorePermit,
    _bytes: OwnedSemaphorePermit,
    _host_bytes: OwnedSemaphorePermit,
}

struct Fact(mass_runtime::RuntimeFact);
impl Fact {
    fn preview(&self) -> bool {
        self.0.transient && self.0.event_type.ends_with("_chunk")
    }
    fn durable(&self) -> bool {
        !self.0.transient
    }
    fn bytes(&self) -> Result<usize> {
        Ok(self.0.event_type.len()
            + self.0.item_id.as_ref().map_or(0, String::len)
            + serde_json::to_vec(&self.0.payload)?.len()
            + serde_json::to_vec(&self.0.evidence)?.len())
    }
    fn commit(self, core: &mut dyn RuntimeEvents) -> Result<()> {
        core.submit(self.0)
    }
}
pub(super) struct FactLane {
    sender: StdMutex<Option<mpsc::Sender<Delivery>>>,
    recorder: Arc<Recorder>,
    connection: String,
    dropped_previews: std::sync::atomic::AtomicUsize,
    high_count: std::sync::atomic::AtomicUsize,
    high_bytes: std::sync::atomic::AtomicUsize,
    capacity: usize,
    backpressured: AtomicBool,
    count: Arc<Semaphore>,
    bytes: Arc<Semaphore>,
    host_bytes: Arc<Semaphore>,
    end: Arc<StdMutex<Option<String>>>,
    done: Arc<AtomicBool>,
    finished: Arc<Notify>,
}
impl FactLane {
    pub(super) fn new(
        host_bytes: Arc<Semaphore>,
        recorder: Arc<Recorder>,
        connection: String,
        capacity: usize,
    ) -> (Self, mpsc::Receiver<Delivery>) {
        let (sender, receiver) = mpsc::channel(capacity);
        (
            Self {
                sender: StdMutex::new(Some(sender)),
                recorder,
                connection,
                dropped_previews: std::sync::atomic::AtomicUsize::new(0),
                high_count: std::sync::atomic::AtomicUsize::new(0),
                high_bytes: std::sync::atomic::AtomicUsize::new(0),
                capacity,
                backpressured: AtomicBool::new(false),
                count: Arc::new(Semaphore::new(capacity)),
                bytes: Arc::new(Semaphore::new(MAX_CONTEXT)),
                host_bytes,
                end: Arc::new(StdMutex::new(None)),
                done: Arc::new(AtomicBool::new(false)),
                finished: Arc::new(Notify::new()),
            },
            receiver,
        )
    }
    pub(super) fn start(
        &self,
        mut receiver: mpsc::Receiver<Delivery>,
        core: Arc<Mutex<dyn RuntimeEvents>>,
        owner: Weak<SessionHandle>,
        delay: Duration,
    ) {
        let end = self.end.clone();
        let done = self.done.clone();
        let finished = self.finished.clone();
        tokio::spawn(async move {
            let mut failed = false;
            while let Some(delivery) = receiver.recv().await {
                if failed {
                    continue;
                } // The sealed writer cannot commit these accepted facts.
                if delivery.fact.durable() {
                    tokio::time::sleep(delay).await;
                }
                let core = core.clone();
                // One executing blocking call per lane; never one task per incoming frame.
                let result = tokio::task::spawn_blocking(move || {
                    let mut core = core.blocking_lock();
                    let Delivery {
                        fact,
                        _count,
                        _bytes,
                        _host_bytes,
                    } = delivery;
                    fact.commit(&mut *core)
                })
                .await;
                let failure = match result {
                    Ok(Ok(())) => None,
                    Ok(Err(e)) => Some(e.to_string()),
                    Err(e) => Some(format!("core worker failed: {e}")),
                };
                if let Some(error) = failure {
                    failed = true;
                    *end.lock().unwrap() =
                        Some(format!("core delivery failed; history incomplete: {error}"));
                    if let Some(owner) = owner.upgrade() {
                        owner.invalidate(&error);
                    }
                }
            }
            let reason = end.lock().unwrap().clone();
            let _ = tokio::task::spawn_blocking(move || {
                let mut core = core.blocking_lock();
                if let Some(reason) = reason {
                    core.disconnect(&reason);
                } else {
                    core.detach();
                }
            })
            .await;
            done.store(true, Ordering::Release);
            finished.notify_waiters();
            if let Some(owner) = owner.upgrade() {
                owner.changed.notify_waiters();
            }
        });
    }
    async fn enqueue(&self, fact: Fact) -> Result<()> {
        let preview = fact.preview();
        let bytes = fact.bytes()?;
        ensure!(
            bytes <= MAX_CONTEXT,
            "normalized fact exceeds 32 MiB application limit; history incomplete"
        );
        let congested = self.count.available_permits() == 0
            || self.bytes.available_permits() < bytes
            || self.host_bytes.available_permits() < bytes;
        let started = tokio::time::Instant::now();
        if congested && !preview {
            self.backpressured.store(true, Ordering::Release);
        }
        if congested && !preview {
            let _=self.recorder.record("local",&json!({"method":"core/backpressure","connectionId":self.connection,"bytes":bytes,"capacity":self.capacity,"durable":fact.durable()}));
        }
        let reserve = async {
            if preview {
                ensure!(
                    self.count.available_permits() > self.capacity / 2,
                    "preview congestion"
                );
                Ok(Delivery {
                    _count: self.count.clone().try_acquire_owned()?,
                    _bytes: self.bytes.clone().try_acquire_many_owned(bytes as u32)?,
                    _host_bytes: self
                        .host_bytes
                        .clone()
                        .try_acquire_many_owned(bytes as u32)?,
                    fact,
                })
            } else {
                Ok::<_, anyhow::Error>(Delivery {
                    _count: self.count.clone().acquire_owned().await?,
                    _bytes: self.bytes.clone().acquire_many_owned(bytes as u32).await?,
                    _host_bytes: self
                        .host_bytes
                        .clone()
                        .acquire_many_owned(bytes as u32)
                        .await?,
                    fact,
                })
            }
        };
        // Finite settlement policy; infinite I/O has no progress guarantee.
        let result = tokio::time::timeout(CONTEXT_TTL, reserve)
            .await
            .context("core backpressure settlement timeout; history incomplete")?
            .and_then(|delivery| {
                let sender = self.sender.lock().unwrap();
                let result = sender
                    .as_ref()
                    .context("core delivery lane closed")?
                    .try_send(delivery)
                    .context("core delivery lane unavailable; history incomplete");
                self.high_count.fetch_max(
                    self.capacity - self.count.available_permits(),
                    Ordering::Relaxed,
                );
                self.high_bytes.fetch_max(
                    MAX_CONTEXT - self.bytes.available_permits(),
                    Ordering::Relaxed,
                );
                result
            });
        if preview && result.is_err() {
            self.dropped_previews.fetch_add(1, Ordering::Relaxed);
            return Ok(());
        }
        self.backpressured.store(false, Ordering::Release);
        if congested && !preview {
            let _=self.recorder.record("local",&json!({"method":"core/backpressureSettled","connectionId":self.connection,"waitMs":started.elapsed().as_millis().to_string(),"accepted":result.is_ok()}));
        }
        result
    }
    pub(super) async fn native_message(
        &self,
        kind: &str,
        key: &str,
        payload: Value,
        preview: bool,
    ) -> Result<()> {
        let event_type = match (kind, preview) {
            ("user", false) => "user.message",
            ("message", false) => "agent.message",
            ("thought", false) => "agent.thinking",
            ("message", true) => "agent.message_chunk",
            ("thought", true) => "agent.thinking_chunk",
            _ => bail!("unsupported message kind"),
        };
        self.enqueue(Fact(normalize_fact(
            event_type, kind, key, payload, preview,
        )?))
        .await
    }
    pub(super) async fn native_tool(&self, key: &str, payload: Value, result: bool) -> Result<()> {
        self.enqueue(Fact(normalize_fact(
            if result {
                "agent.tool_result"
            } else {
                "agent.tool_call"
            },
            "tool",
            key,
            payload,
            false,
        )?))
        .await
    }
    pub(super) async fn native_state(&self, state: Value, _source: Value) -> Result<()> {
        self.enqueue(Fact(mass_runtime::RuntimeFact {
            event_type: "session.state_update".into(),
            item_id: None,
            payload: json!({"state":state}),
            evidence: Value::Null,
            transient: true,
        }))
        .await
    }
    pub(super) fn seal(&self, reason: Option<&str>) {
        // Same lock as enqueue: every accepted delivery precedes the closed sender.
        let mut sender = self.sender.lock().unwrap();
        if let Some(reason) = reason {
            *self.end.lock().unwrap() = Some(reason.into());
        }
        self.count.close();
        self.bytes.close();
        if sender.take().is_some() {
            let _=self.recorder.record("local",&json!({"method":"core/laneSealed","connectionId":self.connection,"capacity":self.capacity,"byteCapacity":MAX_CONTEXT,"highCount":self.high_count.load(Ordering::Relaxed),"highBytes":self.high_bytes.load(Ordering::Relaxed),"droppedPreviews":self.dropped_previews.load(Ordering::Relaxed),"reason":reason}));
        }
    }
    pub(super) fn admits_commands(&self) -> bool {
        self.count.available_permits() > 0 && !self.backpressured.load(Ordering::Acquire)
    }
    pub(super) fn is_idle(&self) -> bool {
        self.count.available_permits() == self.capacity
    }
    pub(super) fn is_drained(&self) -> bool {
        self.done.load(Ordering::Acquire)
    }
    pub(super) async fn drain(&self) {
        loop {
            let finished = self.finished.notified();
            if self.done.load(Ordering::Acquire) {
                break;
            }
            finished.await;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn lane(capacity: usize) -> (Arc<FactLane>, mpsc::Receiver<Delivery>, Arc<Semaphore>) {
        let budget = Arc::new(Semaphore::new(MAX_CONTEXT));
        let (lane, receiver) = FactLane::new(
            budget.clone(),
            Arc::new(Recorder(None, vec![])),
            "test".into(),
            capacity,
        );
        (Arc::new(lane), receiver, budget)
    }

    #[tokio::test]
    async fn full_facts_wait_in_order_previews_drop_and_seal_releases_blocked_producers() {
        let (lane, mut receiver, budget) = lane(2);
        lane.native_message("message", "first", json!({"text":"one"}), false)
            .await
            .unwrap();
        lane.native_message("message", "preview", json!({}), true)
            .await
            .unwrap();
        lane.native_message("message", "second", json!({}), false)
            .await
            .unwrap();
        assert!(!lane.admits_commands());
        let producer_lane = lane.clone();
        let producer = tokio::spawn(async move {
            producer_lane
                .native_message("message", "third", json!({}), false)
                .await
        });
        tokio::task::yield_now().await;
        assert!(!producer.is_finished());
        let first = receiver.recv().await.unwrap();
        assert!(first.fact.0.item_id == Some(mass_runtime::public_id("message", "first")));
        drop(first);
        tokio::time::timeout(Duration::from_secs(1), producer)
            .await
            .unwrap()
            .unwrap()
            .unwrap();
        let second = receiver.recv().await.unwrap();
        assert!(second.fact.0.item_id == Some(mass_runtime::public_id("message", "second")));
        drop(second);
        let third = receiver.recv().await.unwrap();
        assert!(third.fact.0.item_id == Some(mass_runtime::public_id("message", "third")));
        drop(third);
        assert_eq!(lane.dropped_previews.load(Ordering::Relaxed), 1);
        lane.seal(None);
        assert!(lane.native_state(json!("idle"), json!({})).await.is_err());
        assert!(receiver.recv().await.is_none());
        assert_eq!(budget.available_permits(), MAX_CONTEXT);
    }

    #[tokio::test]
    async fn sealing_a_saturated_lane_wakes_waiters_without_accepting_another_fact() {
        let (lane, mut receiver, budget) = lane(1);
        lane.native_tool("first", json!({}), false).await.unwrap();
        let other = lane.clone();
        let waiter = tokio::spawn(async move { other.native_tool("late", json!({}), true).await });
        tokio::task::yield_now().await;
        assert!(!waiter.is_finished());
        lane.seal(Some("host lost"));
        assert!(
            tokio::time::timeout(Duration::from_secs(1), waiter)
                .await
                .unwrap()
                .unwrap()
                .is_err()
        );
        drop(receiver.recv().await.unwrap());
        assert!(receiver.recv().await.is_none());
        assert_eq!(budget.available_permits(), MAX_CONTEXT);
    }

    struct Core {
        calls: Arc<StdMutex<Vec<String>>>,
        fail: bool,
    }
    impl RuntimeEvents for Core {
        fn submit(&mut self, fact: mass_runtime::RuntimeFact) -> Result<()> {
            self.calls.lock().unwrap().push(fact.item_id.unwrap());
            ensure!(!self.fail, "disk failed");
            Ok(())
        }
        fn detach(&mut self) {
            self.calls.lock().unwrap().push("detach".into());
        }
        fn disconnect(&mut self, _: &str) {
            self.calls.lock().unwrap().push("disconnect".into());
        }
    }

    #[tokio::test]
    async fn accepted_facts_drain_before_detach_and_failed_core_never_commits_later_facts() {
        for fail in [false, true] {
            let (lane, receiver, budget) = lane(3);
            let calls = Arc::new(StdMutex::new(Vec::new()));
            for key in ["one", "two"] {
                lane.native_message("message", key, json!({}), false)
                    .await
                    .unwrap();
            }
            lane.start(
                receiver,
                Arc::new(Mutex::new(Core {
                    calls: calls.clone(),
                    fail,
                })),
                Weak::new(),
                Duration::ZERO,
            );
            lane.seal(None);
            tokio::time::timeout(Duration::from_secs(2), lane.drain())
                .await
                .unwrap();
            assert!(lane.is_drained());
            assert_eq!(
                *calls.lock().unwrap(),
                if fail {
                    vec![
                        mass_runtime::public_id("message", "one"),
                        "disconnect".into(),
                    ]
                } else {
                    vec![
                        mass_runtime::public_id("message", "one"),
                        mass_runtime::public_id("message", "two"),
                        "detach".into(),
                    ]
                }
            );
            assert_eq!(budget.available_permits(), MAX_CONTEXT);
        }
    }
}
