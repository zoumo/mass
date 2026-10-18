//! Remote acquisition policy; all protocol and attachment semantics stay in lib.rs.
use super::*;
use url::{Host as UrlHost, Url};

#[derive(Clone, Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct RemoteRegistrationConfig {
    pub url: String,
    #[serde(default)]
    pub auth_ref: Option<String>,
    #[serde(default = "default_close_timeout_ms")]
    pub close_timeout_ms: u64,
    #[serde(default = "default_fact_capacity")]
    pub fact_lane_capacity: usize,
    #[serde(default = "default_sdk_event_bytes")]
    pub sdk_event_bytes: usize,
}
impl RemoteRegistrationConfig {
    pub fn validate(&self) -> Result<()> {
        let url = Url::parse(&self.url).map_err(|_| anyhow::anyhow!("invalid Remote URL"))?;
        ensure!(
            matches!(url.scheme(), "ws" | "wss") && url.host().is_some(),
            "Remote URL must use ws or wss"
        );
        // Secrets have one input: authRef. URL credentials/query/fragment cannot
        // accidentally enter fingerprints, connection errors or diagnostic evidence.
        ensure!(
            url.username().is_empty()
                && url.password().is_none()
                && url.query().is_none()
                && url.fragment().is_none(),
            "Remote URL must not contain credentials, query or fragment"
        );
        if let Some(reference) = &self.auth_ref {
            let name = reference.strip_prefix("env:").unwrap_or("");
            ensure!(
                !name.is_empty() && name.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_'),
                "authRef must use env:NAME"
            );
            let loopback = match url.host() {
                Some(UrlHost::Domain(domain)) => domain.eq_ignore_ascii_case("localhost"),
                Some(UrlHost::Ipv4(ip)) => ip.is_loopback(),
                Some(UrlHost::Ipv6(ip)) => ip.is_loopback(),
                None => false,
            };
            ensure!(
                url.scheme() == "wss" || loopback,
                "Remote authentication requires wss or loopback ws"
            );
        }
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
        Ok(())
    }
    pub fn fingerprint(&self) -> Result<String> {
        self.validate()?;
        Ok(format!("{:x}", Sha256::digest(serde_json::to_vec(self)?)))
    }
    pub fn runtime(self, observation: Observation) -> ProviderRuntime {
        let mut runtime = ProviderRuntime::new(
            Config {
                provider: "codex".into(),
                executable: PathBuf::new(),
                sha256: String::new(),
                close_timeout_ms: self.close_timeout_ms,
                fact_lane_capacity: self.fact_lane_capacity,
                sdk_event_bytes: self.sdk_event_bytes,
                env: BTreeMap::new(),
            },
            observation,
        );
        runtime.remote = Some(self);
        runtime
    }
    pub(super) fn endpoint(&self) -> Result<RemoteAppServerEndpoint> {
        self.validate()?;
        let auth_token = self
            .auth_ref
            .as_ref()
            .map(|reference| {
                let value = std::env::var(reference.strip_prefix("env:").unwrap())
                    .map_err(|_| anyhow::anyhow!("Remote authentication reference unavailable"))?;
                ensure!(
                    !value.is_empty() && value.bytes().all(|b| (0x21..=0x7e).contains(&b)),
                    "Remote authentication reference invalid"
                );
                Ok(value)
            })
            .transpose()?;
        Ok(RemoteAppServerEndpoint::WebSocket {
            websocket_url: self.url.clone(),
            auth_token,
        })
    }
}
pub(super) fn recovery_unsupported() -> anyhow::Error {
    mass_runtime::LifecycleError {
        kind: "unsupported_capability",
        message: "Remote recovery is unsupported: service, account and data ownership cannot be verified",
    }.into()
}

/// Owned local process. Remote acquisition never constructs this resource.
pub(super) struct LocalResources {
    pub child: tokio::process::Child,
    pub socket_directory: tempfile::TempDir,
    pub socket: PathBuf,
    pub stderr_task: tokio::task::JoinHandle<std::io::Result<u64>>,
    pub hash: String,
    pub recovery_scope: Option<String>,
}
impl LocalResources {
    pub async fn reap(mut self) -> bool {
        if let Some(pid) = self.child.id() {
            let _ = nix::sys::signal::kill(
                nix::unistd::Pid::from_raw(pid as i32),
                nix::sys::signal::Signal::SIGTERM,
            );
        }
        let status = match tokio::time::timeout(Duration::from_secs(4), self.child.wait()).await {
            Ok(status) => status,
            Err(_) => {
                let _ = self.child.start_kill();
                self.child.wait().await
            }
        };
        let _ = tokio::time::timeout(Duration::from_secs(2), self.stderr_task).await;
        drop(self.socket_directory);
        status.is_ok()
    }
}
