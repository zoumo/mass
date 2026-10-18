//! Session-local complete-event commits and bounded live fan-out.
use crate::handlers::{Dispatcher, HandlerContext, HandlerTasks, ImmutableEvent};
use mass_journal as storage;
use mass_runtime::RuntimeFact;
use serde_json::{Value, json};
use std::path::PathBuf;
use tokio::sync::broadcast;

pub struct Events {
    name: String,
    session_id: String,
    path: PathBuf,
    seq: u64,
    current: Value,
    live: broadcast::Sender<ImmutableEvent>,
    handlers: Dispatcher,
    failed: bool,
}

impl Events {
    pub fn new(name: String, session_id: String, path: PathBuf) -> Self {
        let (live, _) = broadcast::channel(256);
        Self {
            name,
            session_id,
            path,
            seq: 0,
            current: json!({"state":null,"sessionInfo":null,"configOptions":null,"availableCommands":null,"usage":null}),
            live,
            handlers: Dispatcher::default(),
            failed: false,
        }
    }

    /// Subscribe to future publications; lag disconnects the transport subscriber.
    pub fn subscribe(&self) -> broadcast::Receiver<ImmutableEvent> {
        self.live.subscribe()
    }

    pub fn snapshot(&self) -> &Value {
        &self.current
    }

    /// Publish construction-time Handler lanes only after metadata is durable.
    pub fn activate(&mut self, owner: &HandlerTasks, context: HandlerContext) {
        self.handlers = owner.open(context);
        self.current["configOptions"] = json!([]);
    }

    /// Validate all private records before repairing a partial final append.
    pub fn resume(
        name: String,
        session_id: String,
        path: PathBuf,
        validate_binding: impl FnOnce() -> anyhow::Result<()>,
        validate_record: fn(&str, &Value, &Value) -> anyhow::Result<()>,
    ) -> anyhow::Result<Self> {
        let history = storage::records(&path, 0, usize::MAX)?;
        let mut events = Self::new(name, session_id, path);
        for record in &history {
            let event = &record["event"];
            anyhow::ensure!(
                event["sessionName"] == events.name && event["sessionId"] == events.session_id,
                "history session identity mismatch"
            );
        }
        validate_binding()?;
        for record in &history {
            let event = &record["event"];
            validate_record(&events.session_id, event, &record["evidence"])?;
            events.seq = event["seq"].as_u64().unwrap();
        }
        storage::discard_uncommitted_tail(&events.path)?;
        Ok(events)
    }

    fn emit(&mut self, fact: RuntimeFact) -> anyhow::Result<()> {
        let durable = !fact.transient;
        anyhow::ensure!(fact.payload.is_object(), "fact payload must be an object");
        if durable {
            anyhow::ensure!(
                matches!(
                    fact.event_type.as_str(),
                    "user.message"
                        | "agent.message"
                        | "agent.thinking"
                        | "agent.tool_call"
                        | "agent.tool_result"
                ),
                "unsupported complete fact"
            );
            anyhow::ensure!(
                fact.item_id.as_ref().is_some_and(|id| !id.is_empty()) && fact.evidence.is_object(),
                "complete fact missing identity or opaque evidence"
            );
        }
        anyhow::ensure!(!self.failed, "session event writer failed");
        let mut event = json!({"schemaVersion":2,"sessionName":self.name,"sessionId":self.session_id,"type":fact.event_type,
            "time":time::OffsetDateTime::now_utc().format(&time::format_description::well_known::Rfc3339)?,"payload":fact.payload});
        if let Some(item) = fact.item_id {
            event["itemId"] = json!(item);
        }
        if durable {
            event["seq"] = json!(self.seq + 1);
            storage::validate_public_event(&event)?;
            let record = json!({"event":event,"evidence":fact.evidence});
            if let Err(error) = storage::append_record(&self.path, &record) {
                self.failed = true;
                self.publish(json!({"schemaVersion":2,"sessionName":self.name,"sessionId":self.session_id,"type":"session.error","time":event["time"],"payload":{"reason":"history_write_failed","message":"history append failed"}}));
                return Err(error);
            }
            self.seq += 1;
        }
        if event["type"] == "session.state_update" {
            event["payload"]["previousState"] = self.current["state"].clone();
            self.current["state"] = event["payload"]["state"].clone();
        }
        self.publish(event);
        Ok(())
    }

    fn publish(&mut self, value: Value) {
        let event = ImmutableEvent::new(value);
        let _ = self.live.send(event.clone());
        self.handlers.publish(&event);
    }
}

impl mass_runtime::RuntimeEvents for Events {
    fn submit(&mut self, fact: RuntimeFact) -> anyhow::Result<()> {
        self.emit(fact)
    }
    fn detach(&mut self) {
        self.handlers = Default::default();
    }
    fn disconnect(&mut self, _reason: &str) {
        self.current["state"] = Value::Null;
        self.publish(json!({"schemaVersion":2,"sessionName":self.name,"sessionId":self.session_id,"type":"session.error","time":time::OffsetDateTime::now_utc().format(&time::format_description::well_known::Rfc3339).expect("valid UTC time"),"payload":{"reason":"attachment_unavailable","message":"session attachment unavailable"}}));
        self.handlers = Default::default();
    }
}
