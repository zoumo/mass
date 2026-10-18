//! Controlled real-agent acceptance server; no plugin configuration or fabricated input.
use clap::Parser;
use mass_events::handlers::*;
use serde_json::{Value, json};
use std::{
    fs::OpenOptions,
    io::Write,
    net::SocketAddr,
    path::PathBuf,
    sync::{Arc, Mutex},
    time::Duration,
};

#[derive(Parser)]
struct Args {
    #[arg(long)]
    config: PathBuf,
    #[arg(long, default_value = "127.0.0.1:0")]
    listen: SocketAddr,
}
#[derive(Clone)]
struct Evidence {
    root: PathBuf,
    output: Arc<Mutex<std::fs::File>>,
}
impl Evidence {
    fn record(&self, value: Value) {
        let mut output = self.output.lock().unwrap();
        writeln!(output, "{value}").unwrap();
        output.flush().unwrap();
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
        context: &'a HandlerContext,
        event: &'a ImmutableEvent,
    ) -> HandlerFuture<'a> {
        Box::pin(async move {
            self.evidence.record(json!({"phase":"start","handler":self.name,"event":event.value(),"connection":context.session.connection_id.to_string(),"delivery":format!("{:?}",context.delivery)}));
            let first = std::mem::replace(&mut self.first, false);
            if self.name == "handoff"
                && matches!(context.session.session_name.as_str(), "real" | "target")
                && first
            {
                while !self.evidence.root.join("release-handoff").exists() {
                    tokio::time::sleep(Duration::from_millis(20)).await;
                }
                let result = context
                    .commands
                    .prompt(vec![
                        json!({"type":"text","text":"Reply MASS_STALE_SHOULD_NOT_RUN."}),
                    ])
                    .await;
                self.evidence.record(json!({"phase":"stale","rejected":result.is_err(),"result":format!("{result:?}")}));
                anyhow::ensure!(result.is_err(), "old connection command was accepted");
            }
            if self.name == "slow"
                && matches!(context.session.session_name.as_str(), "real" | "target")
            {
                while !self.evidence.root.join("release-slow").exists() {
                    tokio::time::sleep(Duration::from_millis(20)).await;
                }
            }
            if self.name == "failure" {
                anyhow::bail!("controlled probe failure");
            }
            assert!(self.name != "panic", "controlled probe panic");
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
                let result = context
                    .commands
                    .prompt(vec![
                        json!({"type":"text","text":"Reply exactly MASS_REENTRY_7202. No tools."}),
                    ])
                    .await;
                self.evidence.record(json!({"phase":"command","session":context.session.session_name,"ok":result.is_ok(),"result":format!("{result:?}")}));
                result?;
            }
            if self.name == "command"
                && event.value()["type"] == "agent.message"
                && event.value().to_string().contains("MASS_OTHER_7202")
            {
                let config = context
                    .commands
                    .set_config_option("mass-invalid-probe-7202", "invalid")
                    .await;
                let cancel = context.commands.cancel().await;
                self.evidence.record(json!({"phase":"other-commands","configRejected":config.is_err(),"cancelAccepted":cancel.is_ok()}));
                anyhow::ensure!(
                    config.is_err() && cancel.is_ok(),
                    "extension command contract failed"
                );
            }
            self.evidence
                .record(json!({"phase":"finish","handler":self.name,"event":event.value()}));
            Ok(())
        })
    }
    fn close(&mut self) -> HandlerFuture<'_> {
        Box::pin(async move {
            self.evidence.record(json!({"phase":"close","handler":self.name,"session":self.session.session_name,"connection":self.session.connection_id.to_string()}));
            Ok(())
        })
    }
}
struct CommitProbe(Probe);
impl CommittedEventConsumer for CommitProbe {
    fn consume<'a>(
        &'a mut self,
        context: &'a HandlerContext,
        event: &'a CommittedEvent,
    ) -> HandlerFuture<'a> {
        self.0.handle(context, event.event())
    }
    fn close(&mut self) -> HandlerFuture<'_> {
        self.0.close()
    }
}
#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args = Args::parse();
    tracing_subscriber::fmt()
        .with_writer(std::io::stderr)
        .init();
    let root = args.config.parent().unwrap().to_path_buf();
    let evidence = Evidence {
        output: Arc::new(Mutex::new(
            OpenOptions::new()
                .create(true)
                .append(true)
                .open(root.join("probe.jsonl"))?,
        )),
        root,
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
            capacity.try_into()?,
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
    let committed = evidence.clone();
    handlers.push(HandlerRegistration::committed(
        "committed",
        256.try_into()?,
        move |session| {
            committed.record(
                json!({"phase":"create","handler":"committed","session":session.session_name}),
            );
            Box::new(CommitProbe(Probe {
                name: "committed",
                evidence: committed.clone(),
                session: session.clone(),
                first: true,
            }))
        },
    ));
    let state = Arc::new(mass_daemon::load_service(
        Some(args.config),
        handlers,
        Duration::from_secs(3),
        mass_codex::Observation {
            directory: Some(evidence.root.join("native")),
            ..Default::default()
        },
    )?);
    let listener = tokio::net::TcpListener::bind(args.listen).await?;
    println!("Listening on http://{}", listener.local_addr()?);
    axum::serve(listener, mass_http::router(state.clone()))
        .with_graceful_shutdown(async move {
            let mut terminate =
                tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()).unwrap();
            terminate.recv().await;
            state.shutdown().await;
        })
        .await?;
    Ok(())
}
