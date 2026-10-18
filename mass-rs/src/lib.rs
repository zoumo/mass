//! Daemon composition: configuration, fixed Codex runtimes and session ownership.
use mass_codex::Observation;
use mass_events::handlers::HandlerRegistration;
use mass_sessions::Service;
use serde::Deserialize;
use std::{
    collections::{BTreeMap, BTreeSet},
    path::PathBuf,
    sync::Arc,
    time::Duration,
};

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Config {
    #[serde(default = "default_data_dir")]
    data_dir: PathBuf,
    #[serde(default)]
    runtimes: Vec<RuntimeConfig>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct RuntimeConfig {
    name: String,
    #[serde(rename = "type")]
    runtime_type: String,
    config: serde_json::Value,
    #[serde(default = "enabled_default")]
    enabled: bool,
}
fn enabled_default() -> bool {
    true
}
fn default_data_dir() -> PathBuf {
    PathBuf::from("data")
}

/// Compose configured runtimes without activating native processes.
pub fn load_service(
    path: Option<PathBuf>,
    handlers: Vec<HandlerRegistration>,
    shutdown_timeout: Duration,
    observation: Observation,
) -> anyhow::Result<Service> {
    let mut config = if let Some(path) = path {
        let mut config: Config = serde_json::from_slice(&std::fs::read(&path)?)?;
        if config.data_dir.is_relative() {
            config.data_dir = path.parent().unwrap().join(&config.data_dir);
        }
        config
    } else {
        Config {
            data_dir: default_data_dir(),
            runtimes: Vec::new(),
        }
    };
    config.data_dir = std::path::absolute(config.data_dir)?;
    let mut names = BTreeSet::new();
    let mut providers = BTreeMap::new();
    let mut registrations = Vec::new();
    for runtime in config.runtimes {
        anyhow::ensure!(
            !runtime.name.is_empty()
                && runtime.name.len() <= 64
                && runtime
                    .name
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b)),
            "invalid runtime name"
        );
        anyhow::ensure!(names.insert(runtime.name.clone()), "duplicate runtime name");
        let provider = match runtime.runtime_type.as_str() {
            "codex" => {
                let parameters: mass_codex::RegistrationConfig =
                    serde_json::from_value(runtime.config)
                        .map_err(|_| anyhow::anyhow!("invalid codex configuration"))?;
                parameters.validate()?;
                parameters.runtime(observation.clone())
            }
            "codex-remote" => {
                let parameters: mass_codex::RemoteRegistrationConfig =
                    serde_json::from_value(runtime.config)
                        .map_err(|_| anyhow::anyhow!("invalid codex-remote configuration"))?;
                parameters.validate()?;
                parameters.runtime(observation.clone())
            }
            _ => anyhow::bail!("unsupported runtime type"),
        };
        let fingerprint = mass_runtime::Runtime::fingerprint(&provider)?;
        registrations.push(mass_sessions::Registration {
            name: runtime.name.clone(),
            runtime_type: runtime.runtime_type,
            fingerprint,
            enabled: runtime.enabled,
        });
        providers.insert(runtime.name, Arc::new(provider));
    }
    Service::registered(
        config.data_dir,
        providers,
        registrations,
        handlers,
        shutdown_timeout,
    )
    .map(|service| service.with_runtime_types(vec!["codex".into(), "codex-remote".into()]))
}
