//! Real acceptance server with construction-only wire observation and preview fault gate.
use std::{net::SocketAddr, path::PathBuf, sync::Arc};

use anyhow::Context;
use clap::Parser;
use tokio::net::TcpListener;
use tracing_subscriber::EnvFilter;

#[derive(Parser)]
#[command(version, about = "HTTP service for native agent sessions")]
struct Args {
    /// TCP address for the HTTP listener. Port 0 selects an available port.
    #[arg(long, default_value = "127.0.0.1:24368")]
    listen: SocketAddr,
    #[arg(long)]
    config: Option<PathBuf>,
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args = Args::parse();
    tracing_subscriber::fmt()
        .with_env_filter(
            EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info")),
        )
        .with_writer(std::io::stderr)
        .init();

    let listener = TcpListener::bind(args.listen)
        .await
        .with_context(|| format!("bind HTTP listener at {}", args.listen))?;
    let address = listener.local_addr().context("read bound HTTP address")?;
    let state = Arc::new(mass_daemon::load_service(
        args.config,
        vec![recording_handler()],
        std::time::Duration::from_secs(5),
        mass_codex::Observation {
            directory: std::env::var_os("MASS_NATIVE_EVIDENCE").map(PathBuf::from),
            core_write_delay: std::time::Duration::from_millis(
                std::env::var("MASS_CORE_WRITE_DELAY_MS")
                    .ok()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0),
            ),
            core_write_delay_thread: std::env::var_os("MASS_CORE_WRITE_DELAY_CWD")
                .map(PathBuf::from),
            drop_previews: std::env::var_os("MASS_DROP_PREVIEWS").is_some(),
            thread_response_delay_cwd: std::env::var_os("MASS_THREAD_RESPONSE_DELAY_CWD")
                .map(PathBuf::from),
            thread_request_delay: std::time::Duration::from_millis(
                std::env::var("MASS_THREAD_REQUEST_DELAY_MS")
                    .ok()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0),
            ),
            thread_response_delay: std::time::Duration::from_millis(
                std::env::var("MASS_THREAD_RESPONSE_DELAY_MS")
                    .ok()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0),
            ),
            terminal_delay: std::time::Duration::from_millis(
                std::env::var("MASS_TERMINAL_DELAY_MS")
                    .ok()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0),
            ),
            event_delivery_delay: std::time::Duration::from_millis(
                std::env::var("MASS_EVENT_DELIVERY_DELAY_MS")
                    .ok()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0),
            ),
            disconnect_interrupt: std::env::var_os("MASS_DISCONNECT_INTERRUPT").is_some(),
            closed_delivery_delay: std::time::Duration::from_millis(
                std::env::var("MASS_CLOSED_DELIVERY_DELAY_MS")
                    .ok()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0),
            ),
            command_write_delay: std::time::Duration::from_millis(
                std::env::var("MASS_COMMAND_WRITE_DELAY_MS")
                    .ok()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0),
            ),
            interrupt_response_delay: std::time::Duration::from_millis(
                std::env::var("MASS_INTERRUPT_RESPONSE_DELAY_MS")
                    .ok()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0),
            ),
            approval_response_delay: std::time::Duration::from_millis(
                std::env::var("MASS_APPROVAL_RESPONSE_DELAY_MS")
                    .ok()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0),
            ),
            start_response_delay: std::time::Duration::from_millis(
                std::env::var("MASS_START_RESPONSE_DELAY_MS")
                    .ok()
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(0),
            ),
        },
    )?);
    let app = mass_http::router(state.clone());

    println!("Listening on http://{address}");
    axum::serve(listener, app)
        .with_graceful_shutdown(async move {
            shutdown_signal().await;
            state.shutdown().await;
        })
        .await
        .context("serve HTTP")
}

async fn shutdown_signal() {
    #[cfg(unix)]
    {
        match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(mut terminate) => {
                tokio::select! {
                    result = tokio::signal::ctrl_c() => {
                        if let Err(error) = result { tracing::error!(%error, "failed to install Ctrl-C handler"); }
                    }
                    _ = terminate.recv() => {}
                }
            }
            Err(error) => tracing::error!(%error, "failed to install SIGTERM handler"),
        }
    }
    #[cfg(not(unix))]
    if let Err(error) = tokio::signal::ctrl_c().await {
        tracing::error!(%error, "failed to install Ctrl-C handler");
    }
}

fn recording_handler() -> mass_events::handlers::HandlerRegistration {
    use mass_events::handlers::{HandlerRegistration, Selection};
    let write_lock = std::sync::Arc::new(std::sync::Mutex::new(()));
    HandlerRegistration::new(
        "acceptance-recorder",
        256.try_into().unwrap(),
        Selection::Committed,
        move |_| {
            Box::new(Recorder {
                write_lock: write_lock.clone(),
                path: PathBuf::from(std::env::var_os("MASS_NATIVE_EVIDENCE").unwrap())
                    .join("handler.jsonl"),
                captured_commands: false,
            })
        },
    )
}
struct Recorder {
    write_lock: std::sync::Arc<std::sync::Mutex<()>>,
    path: PathBuf,
    captured_commands: bool,
}
impl mass_events::handlers::EventHandler for Recorder {
    fn handle<'a>(
        &'a mut self,
        context: &'a mass_events::handlers::HandlerContext,
        event: &'a mass_events::handlers::ImmutableEvent,
    ) -> mass_events::handlers::HandlerFuture<'a> {
        Box::pin(async move {
            use std::io::Write;
            if !self.captured_commands
                && context.session.session_name == "target"
                && let Some(directory) = std::env::var_os("MASS_OLD_COMMAND_PROBE")
            {
                self.captured_commands = true;
                let directory = PathBuf::from(directory);
                std::fs::create_dir_all(&directory)?;
                std::fs::write(
                    directory.join("captured.json"),
                    serde_json::to_vec(
                        &serde_json::json!({"connectionId":context.session.connection_id}),
                    )?,
                )?;
                let commands = context.commands.clone();
                tokio::spawn(async move {
                    while !directory.join("release").exists() {
                        tokio::time::sleep(std::time::Duration::from_millis(25)).await;
                    }
                    let prompt = commands.prompt(vec![serde_json::json!({"type":"text","text":"OLD_ATTACHMENT_MUST_NOT_REACH_NATIVE"})]).await;
                    let cancel = commands.cancel().await;
                    let config = commands.set_config_option("model", "stale").await;
                    let outcome = |value: anyhow::Result<serde_json::Value>| match value {
                        Ok(value) => serde_json::json!({"result":value}),
                        Err(error) => serde_json::json!({"error":error.to_string()}),
                    };
                    let _ = std::fs::write(directory.join("result.json"),serde_json::to_vec(&serde_json::json!({"prompt":outcome(prompt.map(|value| serde_json::to_value(value).unwrap()).map_err(anyhow::Error::from)),"cancel":outcome(cancel.map(|()| serde_json::json!({})).map_err(anyhow::Error::from)),"config":outcome(config.map(|()| serde_json::json!({})).map_err(anyhow::Error::from))})).unwrap());
                });
            }
            let _guard = self.write_lock.lock().unwrap();
            let mut file = std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(&self.path)?;
            writeln!(file, "{}", event.value())?;
            file.flush()?;
            Ok(())
        })
    }
}
