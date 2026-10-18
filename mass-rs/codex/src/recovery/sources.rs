//! Read-only provenance for the fixed client's non-user configuration sources.
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{io, path::Path};

pub(super) trait SourceReader {
    fn read(&self, path: &Path) -> io::Result<Option<Vec<u8>>>;
    fn managed_preferences_absent(&self) -> io::Result<bool>;
}
pub(super) struct HostSources;
impl SourceReader for HostSources {
    fn read(&self, path: &Path) -> io::Result<Option<Vec<u8>>> {
        match std::fs::read(path) {
            Ok(bytes) => Ok(Some(bytes)),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
            Err(e) => Err(e),
        }
    }
    fn managed_preferences_absent(&self) -> io::Result<bool> {
        managed_preferences_absent()
    }
}

/// We do not merge administrative ownership settings ourselves. Only the known
/// hooks-only requirements shape is allowed; all other administrative inputs
/// require the official effective-config evaluator and are refused here.
pub(super) fn verified_snapshot(reader: &impl SourceReader) -> anyhow::Result<Value> {
    anyhow::ensure!(
        reader.managed_preferences_absent()?,
        "unproven managed preferences"
    );
    for path in ["/etc/codex/config.toml", "/etc/codex/managed_config.toml"] {
        anyhow::ensure!(
            reader.read(Path::new(path))?.is_none(),
            "unproven system configuration"
        );
    }
    let requirements = reader.read(Path::new("/etc/codex/requirements.toml"))?;
    if let Some(bytes) = &requirements {
        let value: toml::Table = toml::from_str(std::str::from_utf8(bytes)?)?;
        anyhow::ensure!(
            value
                .keys()
                .all(|k| matches!(k.as_str(), "features" | "hooks")),
            "unproven requirements override"
        );
        if let Some(features) = value.get("features") {
            anyhow::ensure!(
                features
                    .as_table()
                    .is_some_and(|t| t.iter().all(|(k, v)| k == "hooks" && v.is_bool())),
                "unproven required feature"
            );
        }
        anyhow::ensure!(
            value.get("hooks").is_none_or(toml::Value::is_table),
            "invalid required hooks"
        );
    }
    Ok(
        json!({"contract":2,"system":"absent","legacyManaged":"absent","managedPreferences":"absent",
        "requirements":requirements.map(|bytes| format!("{:x}",Sha256::digest(bytes)))}),
    )
}

#[cfg(target_os = "macos")]
fn managed_preferences_absent() -> io::Result<bool> {
    use core_foundation::{
        base::{Boolean, TCFType},
        string::{CFString, CFStringRef},
    };
    #[link(name = "CoreFoundation", kind = "framework")]
    unsafe extern "C" {
        fn CFPreferencesAppSynchronize(application_id: CFStringRef) -> Boolean;
        fn CFPreferencesAppValueIsForced(key: CFStringRef, application_id: CFStringRef) -> Boolean;
    }
    let domain = CFString::new("com.openai.codex");
    // Refresh this long-lived process's read cache before observing forced
    // policy. This module never calls a preference setter or changes values.
    if unsafe { CFPreferencesAppSynchronize(domain.as_concrete_TypeRef()) } == 0 {
        return Err(io::Error::other(
            "managed preference observation unavailable",
        ));
    }
    for name in ["config_toml_base64", "requirements_toml_base64"] {
        let key = CFString::new(name);
        // These owned CFStrings remain alive for the complete synchronous call.
        // Match the fixed official loader: ordinary, unforced defaults cannot
        // supply managed layers. Any forced value is deliberately unsupported.
        if unsafe {
            CFPreferencesAppValueIsForced(key.as_concrete_TypeRef(), domain.as_concrete_TypeRef())
        } != 0
        {
            return Ok(false);
        }
    }
    Ok(true)
}
#[cfg(not(target_os = "macos"))]
fn managed_preferences_absent() -> io::Result<bool> {
    Err(io::ErrorKind::Unsupported.into())
}
