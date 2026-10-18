use crate::support::Native;
use mass_events::handlers::*;
use serde_json::{Value, json};
use std::{
    io::Write,
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
    time::Duration,
};

#[derive(Clone)]
struct Evidence {
    root: PathBuf,
    file: Arc<Mutex<std::fs::File>>,
}
impl Evidence {
    fn record(&self, value: Value) {
        let mut file = self.file.lock().unwrap();
        writeln!(file, "{value}").unwrap();
        file.flush().unwrap();
    }
}
struct Probe {
    name: &'static str,
    evidence: Evidence,
    session: SessionIdentity,
    first: bool,
}
impl Drop for Probe {
    fn drop(&mut self) {
        self.evidence.record(
            json!({"phase":"drop","handler":self.name,"session":self.session.session_name}),
        );
    }
}
impl EventHandler for Probe {
    fn handle<'a>(
        &'a mut self,
        ctx: &'a HandlerContext,
        event: &'a ImmutableEvent,
    ) -> HandlerFuture<'a> {
        Box::pin(async move {
            self.evidence.record(json!({"phase":"start","handler":self.name,"event":event.value(),"connection":ctx.session.connection_id.to_string(),"delivery":format!("{:?}",ctx.delivery)}));
            let first = std::mem::replace(&mut self.first, false);
            if self.name == "handoff" && ctx.session.session_name == "target" && first {
                while !self.evidence.root.join("release-handoff").exists() {
                    tokio::time::sleep(Duration::from_millis(20)).await;
                }
                let result = ctx
                    .commands
                    .prompt(vec![
                        json!({"type":"text","text":"MASS_STALE_SHOULD_NOT_RUN"}),
                    ])
                    .await;
                self.evidence
                    .record(json!({"phase":"stale","rejected":result.is_err()}));
                anyhow::ensure!(result.is_err(), "old connection command accepted");
            }
            if self.name == "slow" && ctx.session.session_name == "target" {
                while !self.evidence.root.join("release-slow").exists() {
                    tokio::time::sleep(Duration::from_millis(20)).await;
                }
            }
            anyhow::ensure!(self.name != "failure", "controlled handler failure");
            assert!(self.name != "panic", "controlled handler panic");
            if self.name == "copy" {
                let mut local = event.value().clone();
                local["payload"] = json!({"changed":"LOCAL_COPY_ONLY"});
                self.evidence
                    .record(json!({"phase":"copy","local":local,"original":event.value()}));
            }
            if self.name == "command"
                && event.value()["type"] == "agent.message"
                && event.value().to_string().contains("MASS_HANDLER_7202")
            {
                while !self.evidence.root.join("release-command").exists() {
                    tokio::time::sleep(Duration::from_millis(20)).await;
                }
                let result = ctx
                    .commands
                    .prompt(vec![
                        json!({"type":"text","text":"Reply exactly MASS_REENTRY_7202. No tools."}),
                    ])
                    .await;
                self.evidence.record(json!({"phase":"command","session":ctx.session.session_name,"ok":result.is_ok()}));
                result?;
            }
            self.evidence
                .record(json!({"phase":"finish","handler":self.name,"event":event.value()}));
            Ok(())
        })
    }
    fn close(&mut self) -> HandlerFuture<'_> {
        Box::pin(async move {
            self.evidence.record(
                json!({"phase":"close","handler":self.name,"session":self.session.session_name}),
            );
            Ok(())
        })
    }
}
struct CommitProbe(Probe);
impl CommittedEventConsumer for CommitProbe {
    fn consume<'a>(
        &'a mut self,
        ctx: &'a HandlerContext,
        event: &'a CommittedEvent,
    ) -> HandlerFuture<'a> {
        self.0.handle(ctx, event.event())
    }
    fn close(&mut self) -> HandlerFuture<'_> {
        self.0.close()
    }
}
pub fn registrations(root: &Path) -> Vec<HandlerRegistration> {
    let evidence = Evidence {
        root: root.into(),
        file: Arc::new(Mutex::new(
            std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(root.join("probe.jsonl"))
                .unwrap(),
        )),
    };
    let mut handlers = Vec::new();
    for (name, capacity, selection) in [
        ("healthy", 256, Selection::All),
        ("handoff", 256, Selection::Committed),
        (
            "filtered",
            256,
            Selection::Types(vec!["agent.message".into()]),
        ),
        ("copy", 256, Selection::Committed),
        ("slow", 1, Selection::All),
        (
            "failure",
            256,
            Selection::Types(vec!["agent.message".into()]),
        ),
        ("panic", 256, Selection::Types(vec!["agent.message".into()])),
        (
            "command",
            256,
            Selection::Types(vec!["agent.message".into()]),
        ),
    ] {
        let evidence = evidence.clone();
        handlers.push(HandlerRegistration::new(
            name,
            capacity.try_into().unwrap(),
            selection,
            move |session| {
                evidence.record(
                    json!({"phase":"create","handler":name,"session":session.session_name}),
                );
                Box::new(Probe {
                    name,
                    evidence: evidence.clone(),
                    session: session.clone(),
                    first: true,
                })
            },
        ));
    }
    handlers.push(HandlerRegistration::committed(
        "committed",
        256.try_into().unwrap(),
        move |session| {
            evidence.record(
                json!({"phase":"create","handler":"committed","session":session.session_name}),
            );
            Box::new(CommitProbe(Probe {
                name: "committed",
                evidence: evidence.clone(),
                session: session.clone(),
                first: true,
            }))
        },
    ));
    handlers
}
fn probes(h: &Native) -> Vec<Value> {
    std::fs::read_to_string(h.root.join("probe.jsonl"))
        .unwrap()
        .split_inclusive('\n')
        .filter(|line| line.ends_with('\n'))
        .map(|line| serde_json::from_str(line).unwrap())
        .collect()
}
pub async fn run(h: &Native) {
    h.new_session("target").await;
    h.new_session("peer").await;
    let live = h.live("target").await;
    let peer = h.live("peer").await;
    h.prompt("target", "Without tools reply exactly MASS_HANDLER_7202.")
        .await;
    h.idle("target").await;
    std::fs::write(h.root.join("release-command"), []).unwrap();
    h.wait(|| {
        probes(h)
            .iter()
            .any(|r| r["phase"] == "command" && r["ok"] == true)
            .then_some(())
    })
    .await;
    h.idle("target").await;
    h.prompt("peer", "Without tools reply PEER06_HANDLER.")
        .await;
    h.idle("peer").await;
    for (name, live) in [("target", &live), ("peer", &peer)] {
        let history = h.history(name).await;
        h.assert_history(name).await;
        h.wait(|| {
            (live
                .rows()
                .iter()
                .filter(|e| e.get("seq").is_some())
                .cloned()
                .collect::<Vec<_>>()
                == history)
                .then_some(())
        })
        .await;
        h.wait(|| {
            ["healthy", "committed", "copy"]
                .iter()
                .all(|handler| {
                    probes(h)
                        .iter()
                        .filter(|r| {
                            r["phase"] == "finish"
                                && r["handler"] == *handler
                                && r["event"]["sessionName"] == name
                                && r["event"].get("seq").is_some()
                        })
                        .map(|r| r["event"].clone())
                        .collect::<Vec<_>>()
                        == history
                })
                .then_some(())
        })
        .await;
        let observed = probes(h);
        let filtered: Vec<_> = observed
            .iter()
            .filter(|r| {
                r["phase"] == "finish"
                    && r["handler"] == "filtered"
                    && r["event"]["sessionName"] == name
            })
            .map(|r| r["event"].clone())
            .collect();
        assert_eq!(
            filtered,
            history
                .iter()
                .filter(|e| e["type"] == "agent.message")
                .cloned()
                .collect::<Vec<_>>()
        );
        assert!(
            observed
                .iter()
                .filter(|r| r["phase"] == "copy" && r["original"]["sessionName"] == name)
                .all(|r| history.contains(&r["original"])
                    && r["local"]["payload"] != r["original"]["payload"])
        );
    }
    for handler in ["failure", "panic"] {
        h.wait(|| {
            probes(h)
                .iter()
                .any(|r| r["phase"] == "close" && r["handler"] == handler)
                .then_some(())
        })
        .await;
        assert_eq!(
            probes(h)
                .iter()
                .filter(|r| r["phase"] == "start"
                    && r["handler"] == handler
                    && r["event"]["sessionName"] == "target")
                .count(),
            1,
            "failed handlers must disable only their own lane"
        );
    }
    std::fs::write(h.root.join("release-slow"), []).unwrap();
    h.wait(|| {
        probes(h)
            .iter()
            .any(|r| r["phase"] == "close" && r["handler"] == "slow" && r["session"] == "target")
            .then_some(())
    })
    .await;
    assert!(
        probes(h)
            .iter()
            .filter(|r| r["phase"] == "finish"
                && r["handler"] == "slow"
                && r["event"]["sessionName"] == "target")
            .count()
            <= 2,
        "capacity one plus one executing; overflow closes the lane"
    );
    std::fs::write(
        h.root.join("result.json"),
        json!({"passed":true,"migratedFrom":"codex_faults_e2e.py","case":"handlers"}).to_string(),
    )
    .unwrap();
}
