//! Local ownership evidence. Never retain credentials or infer ownership from a PID.
mod sources;
use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{collections::BTreeMap, os::unix::fs::MetadataExt, path::PathBuf};

/// File-backed ChatGPT identities can be checked before starting an Agent. Other
/// authentication modes keep their history but cannot claim verified cold recovery.
pub(super) fn scope(environment: &BTreeMap<String, String>) -> Option<String> {
    scope_from_sources(environment, &sources::HostSources)
}
fn scope_from_sources(
    environment: &BTreeMap<String, String>,
    sources: &impl sources::SourceReader,
) -> Option<String> {
    let snapshot = sources::verified_snapshot(sources).ok()?;
    scope_inner(environment, &snapshot).ok()
}

fn data_home(environment: &BTreeMap<String, String>) -> anyhow::Result<PathBuf> {
    let effective = |key: &str| {
        environment
            .get(key)
            .cloned()
            .or_else(|| std::env::var(key).ok())
    };
    let home = effective("CODEX_HOME")
        .filter(|s| !s.trim().is_empty())
        .map(PathBuf::from)
        .or_else(|| effective("HOME").map(|p| PathBuf::from(p).join(".codex")))
        .ok_or_else(|| anyhow::anyhow!("missing data home"))?;
    anyhow::ensure!(home.is_absolute(), "relative data home");
    let home = home.canonicalize()?;
    Ok(home)
}

fn scope_inner(
    environment: &BTreeMap<String, String>,
    source_snapshot: &Value,
) -> anyhow::Result<String> {
    let effective = |key: &str| {
        environment
            .get(key)
            .cloned()
            .or_else(|| std::env::var(key).ok())
    };
    anyhow::ensure!(
        [
            "OPENAI_API_KEY",
            "CODEX_API_KEY",
            "CODEX_ACCESS_TOKEN",
            "CODEX_AUTH_JSON",
            "CODEX_APP_SERVER_TEST_USER_CONFIG_FILE",
            "OPENAI_BASE_URL",
            "CODEX_REFRESH_TOKEN_URL_OVERRIDE",
            "CODEX_APP_SERVER_LOGIN_CLIENT_ID"
        ]
        .iter()
        .all(|key| effective(key).is_none()),
        "unverifiable authentication override"
    );
    let home = data_home(environment)?;
    let metadata = home.metadata()?;
    anyhow::ensure!(metadata.is_dir(), "invalid data home");
    let config_bytes = match std::fs::read(home.join("config.toml")) {
        Ok(bytes) => bytes,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Vec::new(),
        Err(e) => return Err(e.into()),
    };
    let config: toml::Value = toml::from_str(std::str::from_utf8(&config_bytes)?)?;
    anyhow::ensure!(
        config
            .get("cli_auth_credentials_store")
            .is_none_or(|v| v.as_str() == Some("file"))
            && config
                .get("model_provider")
                .is_none_or(|v| v.as_str() == Some("openai"))
            && config.get("profile").is_none()
            && config
                .get("model_providers")
                .and_then(|v| v.get("openai"))
                .is_none(),
        "unverifiable authentication source"
    );
    let sqlite = config
        .get("sqlite_home")
        .and_then(toml::Value::as_str)
        .map(str::to_owned)
        .or_else(|| effective("CODEX_SQLITE_HOME"))
        .map(PathBuf::from)
        .unwrap_or_else(|| home.clone());
    anyhow::ensure!(sqlite.is_absolute(), "relative database home");
    let sqlite = sqlite.canonicalize()?;
    let sqlite_metadata = sqlite.metadata()?;
    anyhow::ensure!(sqlite_metadata.is_dir(), "invalid database home");
    let auth: Value = serde_json::from_slice(&std::fs::read(home.join("auth.json"))?)?;
    anyhow::ensure!(
        auth["auth_mode"].as_str() == Some("chatgpt")
            && (auth["OPENAI_API_KEY"].is_null() || auth["OPENAI_API_KEY"].as_str() == Some("")),
        "unsupported authentication mode"
    );
    let account = auth["tokens"]["account_id"]
        .as_str()
        .filter(|s| !s.is_empty())
        .ok_or_else(|| anyhow::anyhow!("missing persistent account"))?;
    let token = auth["tokens"]["id_token"]
        .as_str()
        .ok_or_else(|| anyhow::anyhow!("missing identity claims"))?;
    let claims: Value = serde_json::from_slice(
        &URL_SAFE_NO_PAD.decode(
            token
                .split('.')
                .nth(1)
                .ok_or_else(|| anyhow::anyhow!("invalid identity claims"))?,
        )?,
    )?;
    // The fixed cloud-config service only skips fetching for non-workspace
    // plans. Unknown/missing and all workspace claims cannot prove absence of
    // cloud requirements; do not treat an empty/stale cache as that proof.
    let plan = claims["https://api.openai.com/auth"]["chatgpt_plan_type"]
        .as_str()
        .map(str::to_ascii_lowercase)
        .ok_or_else(|| anyhow::anyhow!("unproven cloud configuration"))?;
    anyhow::ensure!(
        matches!(
            plan.as_str(),
            "free" | "go" | "plus" | "pro" | "prolite" | "promax"
        ),
        "unproven cloud configuration"
    );
    let subject = claims["sub"]
        .as_str()
        .filter(|s| !s.is_empty())
        .ok_or_else(|| anyhow::anyhow!("missing persistent user"))?;
    // Only non-secret identity/configuration material is digested. Token refresh
    // does not change ownership; changing account, user or directory does.
    Ok(format!(
        "{:x}",
        Sha256::digest(serde_json::to_vec(&json!({
            "sources":source_snapshot,"cloud":"ineligible-personal-plan","plan":plan,
            "home":home,"device":metadata.dev(),"inode":metadata.ino(),"owner":metadata.uid(),
            "database":sqlite,"databaseDevice":sqlite_metadata.dev(),"databaseInode":sqlite_metadata.ino(),
            "account":account,"user":subject,"config":format!("{:x}",Sha256::digest(config_bytes))
        }))?)
    ))
}

/// Without the official effective-config evaluator, profile and project auth
/// overrides cannot be proved equivalent to the file-backed global account.
/// Refuse recovery rather than using a global account as a substitute.
pub(super) fn session_scope(
    environment: &BTreeMap<String, String>,
    cwd: &std::path::Path,
    global: Option<&str>,
) -> Option<String> {
    let global = global?;
    let cwd = cwd.canonicalize().ok()?;
    let global_config = data_home(environment).ok()?.join("config.toml");
    let cwd_config = cwd.join("config.toml");
    if cwd_config.try_exists().ok()? && cwd_config.canonicalize().ok()? != global_config {
        return None;
    }

    for ancestor in cwd.ancestors() {
        let path = ancestor.join(".codex/config.toml");
        if path.try_exists().ok()? && path.canonicalize().ok()? != global_config {
            return None;
        }
    }
    Some(format!(
        "{:x}",
        Sha256::digest(serde_json::to_vec(&(global, cwd)).ok()?)
    ))
}

/// A reused process cannot certify a new attachment with cached provenance
/// after current administrative/account inputs stop matching its original proof.
pub(super) fn session_scope_for_host(
    environment: &BTreeMap<String, String>,
    cwd: &std::path::Path,
    host: Option<&str>,
    current: Option<&str>,
) -> Option<String> {
    let host = host?;
    let current = current?;
    if host != current {
        return None;
    }
    session_scope(environment, cwd, Some(current))
}

/// A malformed resume result can still require release. Its provisional route
/// must never publish into the saved Session while that release is settling.
pub(super) fn events_for(
    expected: Option<&str>,
    factory: mass_runtime::EventFactory,
) -> mass_runtime::EventFactory {
    let Some(expected) = expected.map(str::to_owned) else {
        return factory;
    };
    std::sync::Arc::new(move |id| {
        if id == expected {
            factory(id)
        } else {
            std::sync::Arc::new(tokio::sync::Mutex::new(Unpublished))
        }
    })
}
struct Unpublished;
impl mass_runtime::RuntimeEvents for Unpublished {
    fn submit(&mut self, _: mass_runtime::RuntimeFact) -> anyhow::Result<()> {
        Ok(())
    }
    fn detach(&mut self) {}
    fn disconnect(&mut self, _: &str) {}
}

#[cfg(test)]
mod tests {
    use super::*;
    fn auth(home: &std::path::Path, account: &str, user: &str, credential: &str) {
        let claims = URL_SAFE_NO_PAD.encode(
            json!({"sub":user,"https://api.openai.com/auth":{"chatgpt_plan_type":"pro"}})
                .to_string(),
        );
        std::fs::write(
            home.join("auth.json"),
            json!({"auth_mode":"chatgpt","OPENAI_API_KEY":null,
            "tokens":{"account_id":account,"id_token":format!("header.{claims}.signature"),
            "access_token":credential,"refresh_token":credential}})
            .to_string(),
        )
        .unwrap();
    }
    #[test]
    fn local_scope_tracks_account_user_data_and_configuration_without_retaining_tokens() {
        let home = tempfile::tempdir().unwrap();
        let env = BTreeMap::from([(
            "CODEX_HOME".into(),
            home.path().to_string_lossy().into_owned(),
        )]);
        auth(home.path(), "account", "user", "secret-one");
        let original = scope(&env).unwrap();
        // Codex file auth may retain an empty API-key placeholder in ChatGPT mode.
        let mut placeholder: Value =
            serde_json::from_slice(&std::fs::read(home.path().join("auth.json")).unwrap()).unwrap();
        placeholder["OPENAI_API_KEY"] = json!("");
        std::fs::write(home.path().join("auth.json"), placeholder.to_string()).unwrap();
        assert_eq!(scope(&env).as_deref(), Some(original.as_str()));
        placeholder["OPENAI_API_KEY"] = json!("fixture-key");
        std::fs::write(home.path().join("auth.json"), placeholder.to_string()).unwrap();
        assert!(scope(&env).is_none());
        auth(home.path(), "account", "user", "secret-two");
        assert_eq!(scope(&env).unwrap(), original);
        for (account, user) in [("other", "user"), ("account", "other")] {
            auth(home.path(), account, user, "secret-two");
            assert_ne!(scope(&env).unwrap(), original);
        }
        auth(home.path(), "account", "user", "secret-one");
        std::fs::write(home.path().join("config.toml"), "model = 'other'\n").unwrap();
        assert_ne!(scope(&env).unwrap(), original);
        std::fs::write(
            home.path().join("config.toml"),
            "cli_auth_credentials_store = 'keyring'\n",
        )
        .unwrap();
        assert!(scope(&env).is_none());
        std::fs::remove_file(home.path().join("config.toml")).unwrap();
        let moved = home.path().join("other-home");
        std::fs::create_dir(&moved).unwrap();
        auth(&moved, "account", "user", "secret-one");
        assert_ne!(
            scope(&BTreeMap::from([(
                "CODEX_HOME".into(),
                moved.to_string_lossy().into_owned()
            )]))
            .unwrap(),
            original
        );
        std::fs::remove_file(home.path().join("auth.json")).unwrap();
        assert!(scope(&env).is_none());
    }
    #[test]
    fn runtime_recovery_rejects_changed_account_data_and_versions_before_activation() {
        use mass_runtime::Runtime;
        let home = tempfile::tempdir().unwrap();
        auth(home.path(), "account", "user", "fixture-secret");
        let env = BTreeMap::from([(
            "CODEX_HOME".into(),
            home.path().to_string_lossy().into_owned(),
        )]);
        let runtime = crate::ProviderRuntime::new(serde_json::from_value(json!({
            "provider":"codex","executable":"/never-start-recovery-test","sha256":"a".repeat(64),"env":env
        })).unwrap(),Default::default());
        let id = "018f0000-0000-7000-8000-000000000001";
        let binding = json!({"adapter":"codex","version":2,"sessionId":id,"scope":session_scope(&env,home.path(),scope(&env).as_deref()).unwrap(),
            "runtime":{"provider":"codex","connectorVersion":"2","version":crate::VERSION,
            "transport":"app_server_unix_websocket","executable":"/never-start-recovery-test","sha256":"a".repeat(64)}});
        runtime
            .validate_recovery(id, &binding, home.path())
            .unwrap();
        let mut unproven_v1 = binding.clone();
        unproven_v1["version"] = json!(1);
        assert!(
            runtime
                .validate_recovery(id, &unproven_v1, home.path())
                .is_err()
        );
        for pointer in ["/version", "/sessionId", "/scope", "/runtime/sha256"] {
            let mut bad = binding.clone();
            *bad.pointer_mut(pointer).unwrap() = json!("unsupported");
            assert!(
                runtime.validate_recovery(id, &bad, home.path()).is_err(),
                "{pointer}"
            );
        }
        auth(home.path(), "other-account", "user", "fixture-secret");
        assert!(
            runtime
                .validate_recovery(id, &binding, home.path())
                .is_err()
        );
        auth(home.path(), "account", "user", "fixture-secret");
        runtime
            .validate_recovery(id, &binding, home.path())
            .unwrap();
        let old = home.path().join("original-auth");
        std::fs::rename(home.path().join("auth.json"), old).unwrap();
        assert!(
            runtime
                .validate_recovery(id, &binding, home.path())
                .is_err()
        );
        assert!(runtime.host.try_lock().unwrap().is_none());
    }
    #[tokio::test]
    async fn mismatched_resume_cannot_route_facts_into_the_saved_session_during_cleanup() {
        use std::sync::{
            Arc,
            atomic::{AtomicUsize, Ordering},
        };
        let calls = Arc::new(AtomicUsize::new(0));
        let count = calls.clone();
        let factory: mass_runtime::EventFactory = Arc::new(move |_| {
            count.fetch_add(1, Ordering::AcqRel);
            Arc::new(tokio::sync::Mutex::new(Unpublished))
        });
        let guarded = events_for(Some("saved-id"), factory);
        let wrong = guarded("unexpected-id");
        wrong
            .lock()
            .await
            .submit(mass_runtime::RuntimeFact {
                event_type: "agent.message".into(),
                item_id: Some("foreign".into()),
                payload: json!({}),
                evidence: json!({}),
                transient: false,
            })
            .unwrap();
        assert_eq!(calls.load(Ordering::Acquire), 0);
        let _original = guarded("saved-id");
        assert_eq!(calls.load(Ordering::Acquire), 1);
    }
    #[test]
    fn profile_project_and_changed_working_directory_cannot_reuse_global_account_provenance() {
        let root = tempfile::tempdir().unwrap();
        let home = root.path().join("global");
        std::fs::create_dir(&home).unwrap();
        let cwd = root.path().join("work");
        std::fs::create_dir(&cwd).unwrap();
        auth(&home, "account", "user", "fixture-secret");
        let env = BTreeMap::from([("CODEX_HOME".into(), home.to_string_lossy().into_owned())]);
        let global = scope(&env).unwrap();
        let original = session_scope(&env, &cwd, Some(&global)).unwrap();
        assert_ne!(session_scope(&env, &home, Some(&global)).unwrap(), original);
        std::fs::create_dir(cwd.join(".codex")).unwrap();
        std::fs::write(cwd.join(".codex/config.toml"), "model_provider='custom'\n").unwrap();
        assert!(session_scope(&env, &cwd, Some(&global)).is_none());
        std::fs::remove_file(cwd.join(".codex/config.toml")).unwrap();
        assert_eq!(session_scope(&env, &cwd, Some(&global)).unwrap(), original);
        for config in [
            "profile='custom'\n",
            "[model_providers.openai]\nenv_key='OTHER_ACCOUNT'\n",
        ] {
            std::fs::write(home.join("config.toml"), config).unwrap();
            assert!(scope(&env).is_none());
        }
    }
    struct FixtureSources {
        root: PathBuf,
        managed: bool,
        denied: bool,
    }
    impl sources::SourceReader for FixtureSources {
        fn read(&self, path: &std::path::Path) -> std::io::Result<Option<Vec<u8>>> {
            if self.denied {
                return Err(std::io::ErrorKind::PermissionDenied.into());
            }
            match std::fs::read(self.root.join(path.file_name().unwrap())) {
                Ok(bytes) => Ok(Some(bytes)),
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
                Err(e) => Err(e),
            }
        }
        fn managed_preferences_absent(&self) -> std::io::Result<bool> {
            if self.denied {
                Err(std::io::ErrorKind::PermissionDenied.into())
            } else {
                Ok(!self.managed)
            }
        }
    }
    #[test]
    fn administrative_sources_cannot_silently_replace_file_auth_or_data_ownership() {
        let root = tempfile::tempdir().unwrap();
        let home = root.path().join("home");
        std::fs::create_dir(&home).unwrap();
        auth(&home, "account", "user", "dummy-secret");
        let env = BTreeMap::from([("CODEX_HOME".into(), home.to_string_lossy().into_owned())]);
        let fixture = FixtureSources {
            root: root.path().into(),
            managed: false,
            denied: false,
        };
        let original = scope_from_sources(&env, &fixture).unwrap();
        for (name, text) in [
            (
                "requirements.toml",
                "cli_auth_credentials_store='keyring'\n",
            ),
            ("requirements.toml", "sqlite_home='/other-data'\n"),
            ("requirements.toml", "model_provider='other'\n"),
            ("config.toml", "sqlite_home='/system-data'\n"),
            ("managed_config.toml", "cli_auth_credentials_store='auto'\n"),
            ("requirements.toml", "[features]\nunknown_feature=true\n"),
            ("requirements.toml", "this is invalid TOML"),
        ] {
            std::fs::write(root.path().join(name), text).unwrap();
            let original_auth = std::fs::read(home.join("auth.json")).unwrap();
            let original_source = std::fs::read(root.path().join(name)).unwrap();
            assert!(
                scope_from_sources(&env, &fixture).is_none(),
                "unproven source: {name} {text}"
            );
            assert_eq!(
                std::fs::read(home.join("auth.json")).unwrap(),
                original_auth
            );
            assert_eq!(
                std::fs::read(root.path().join(name)).unwrap(),
                original_source
            );
            std::fs::remove_file(root.path().join(name)).unwrap();
            assert_eq!(scope_from_sources(&env, &fixture).unwrap(), original);
        }
        assert!(
            scope_from_sources(
                &env,
                &FixtureSources {
                    root: root.path().into(),
                    managed: true,
                    denied: false
                }
            )
            .is_none()
        );
        assert!(
            scope_from_sources(
                &env,
                &FixtureSources {
                    root: root.path().into(),
                    managed: false,
                    denied: true
                }
            )
            .is_none()
        );
    }

    #[test]
    fn benign_requirements_are_bound_and_cloud_eligibility_is_not_inferred_from_user_config() {
        let root = tempfile::tempdir().unwrap();
        let home = root.path().join("home");
        std::fs::create_dir(&home).unwrap();
        let env = BTreeMap::from([("CODEX_HOME".into(), home.to_string_lossy().into_owned())]);
        let fixture = FixtureSources {
            root: root.path().into(),
            managed: false,
            denied: false,
        };
        auth(&home, "account", "user", "dummy-secret");
        let plain = scope_from_sources(&env, &fixture).unwrap();
        std::fs::write(
            root.path().join("requirements.toml"),
            "[features]\nhooks=true\n[hooks]\n",
        )
        .unwrap();
        let hooked = scope_from_sources(&env, &fixture).unwrap();
        assert_ne!(plain, hooked);
        std::fs::write(
            root.path().join("requirements.toml"),
            "[features]\nhooks=false\n[hooks]\n",
        )
        .unwrap();
        assert_ne!(scope_from_sources(&env, &fixture).unwrap(), hooked);
        for plan in ["business", "enterprise", "edu", "team", "unknown", ""] {
            let claims = URL_SAFE_NO_PAD.encode(
                json!({"sub":"user","https://api.openai.com/auth":{"chatgpt_plan_type":plan}})
                    .to_string(),
            );
            let path = home.join("auth.json");
            let mut value: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
            value["tokens"]["id_token"] = json!(format!("header.{claims}.signature"));
            std::fs::write(path, value.to_string()).unwrap();
            assert!(
                scope_from_sources(&env, &fixture).is_none(),
                "unproven cloud source for {plan}"
            );
        }
        auth(&home, "account", "user", "new-refreshed-secret");
        assert!(scope_from_sources(&env, &fixture).is_some());
    }
    #[test]
    fn reused_hosts_cannot_certify_new_bindings_from_stale_source_proof() {
        let root = tempfile::tempdir().unwrap();
        let home = root.path().join("home");
        std::fs::create_dir(&home).unwrap();
        auth(&home, "account", "user", "dummy-secret");
        let env = BTreeMap::from([("CODEX_HOME".into(), home.to_string_lossy().into_owned())]);
        let fixture = FixtureSources {
            root: root.path().into(),
            managed: false,
            denied: false,
        };
        let original = scope_from_sources(&env, &fixture).unwrap();
        assert!(session_scope_for_host(&env, &home, Some(&original), Some(&original)).is_some());
        std::fs::write(
            root.path().join("requirements.toml"),
            "cli_auth_credentials_store='keyring'\n",
        )
        .unwrap();
        let current = scope_from_sources(&env, &fixture);
        assert!(session_scope_for_host(&env, &home, Some(&original), current.as_deref()).is_none());
        std::fs::remove_file(root.path().join("requirements.toml")).unwrap();
        std::fs::write(
            home.join("config.toml"),
            "openai_base_url='https://explicit-inference.example/v1'\n",
        )
        .unwrap();
        let changed = scope_from_sources(&env, &fixture).unwrap();
        assert_ne!(changed, original);
        assert!(session_scope_for_host(&env, &home, Some(&original), Some(&changed)).is_none());
        assert!(session_scope_for_host(&env, &home, Some(&changed), Some(&changed)).is_some());
    }
}
