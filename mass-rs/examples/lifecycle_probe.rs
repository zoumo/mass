//! Real-agent lifecycle acceptance server, using only public construction APIs.
use clap::Parser;
use mass_events::handlers::*;
use serde_json::json;
use std::{fs::OpenOptions, io::Write, net::SocketAddr, path::PathBuf, sync::Arc, time::Duration};

#[derive(Parser)]
struct Args {
    #[arg(long)]
    config: PathBuf,
    #[arg(long, default_value = "127.0.0.1:0")]
    listen: SocketAddr,
    #[arg(long, default_value_t = 1500)]
    consumer_timeout_ms: u64,
}

struct Audit {
    root: PathBuf,
}
impl Audit {
    fn record(&self, value: serde_json::Value) {
        let mut output = OpenOptions::new()
            .create(true)
            .append(true)
            .open(self.root.join("probe.jsonl"))
            .unwrap();
        writeln!(output, "{value}").unwrap();
        output.flush().unwrap();
    }
}
impl CommittedEventConsumer for Audit {
    fn consume<'a>(
        &'a mut self,
        context: &'a HandlerContext,
        event: &'a CommittedEvent,
    ) -> HandlerFuture<'a> {
        Box::pin(async move {
            self.record(json!({"phase":"start","event":event.event().value(),"connection":context.session.connection_id.to_string()}));
            while !self.root.join("release-consumer").exists() {
                tokio::time::sleep(Duration::from_millis(10)).await;
            }
            if self.root.join("check-commands").exists() {
                let prompt = context
                    .commands
                    .prompt(vec![
                        json!({"type":"text","text":"MASS_STALE_7303_MUST_NOT_RUN"}),
                    ])
                    .await;
                let cancel = context.commands.cancel().await;
                let config = context
                    .commands
                    .set_config_option("mass-stale-7303", "invalid")
                    .await;
                self.record(json!({"phase":"commands","connection":context.session.connection_id.to_string(),"prompt":format!("{prompt:?}"),"cancel":format!("{cancel:?}"),"config":format!("{config:?}"),"allRejected":prompt.is_err() && cancel.is_err() && config.is_err()}));
                anyhow::ensure!(
                    prompt.is_err() && cancel.is_err() && config.is_err(),
                    "stale commands accepted"
                );
            }
            self.record(json!({"phase":"finish","event":event.event().value()}));
            Ok(())
        })
    }
    fn close(&mut self) -> HandlerFuture<'_> {
        Box::pin(async move {
            self.record(json!({"phase":"close"}));
            Ok(())
        })
    }
}
impl Drop for Audit {
    fn drop(&mut self) {
        self.record(json!({"phase":"drop"}));
    }
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args = Args::parse();
    tracing_subscriber::fmt()
        .with_writer(std::io::stderr)
        .init();
    let root = args.config.parent().unwrap().to_path_buf();
    let audit_root = root.clone();
    let handlers = vec![HandlerRegistration::committed(
        "audit",
        256.try_into()?,
        move |_| {
            Box::new(Audit {
                root: audit_root.clone(),
            })
        },
    )];
    let state = Arc::new(mass_daemon::load_service(
        Some(args.config),
        handlers,
        Duration::from_millis(args.consumer_timeout_ms),
        Default::default(),
    )?);
    let listener = tokio::net::TcpListener::bind(args.listen).await?;
    println!("Listening on http://{}", listener.local_addr()?);
    axum::serve(listener, mass_http::router(state.clone()))
        .with_graceful_shutdown(async move {
            let mut terminate =
                tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()).unwrap();
            terminate.recv().await;
            std::fs::write(root.join("shutdown-started"), b"signal received").unwrap();
            state.shutdown().await;
        })
        .await?;
    Ok(())
}
