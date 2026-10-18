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
        Vec::new(),
        std::time::Duration::from_secs(5),
        Default::default(),
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
