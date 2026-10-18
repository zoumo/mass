//! Codex module e2e through its public lifecycle and RuntimeEvents boundary.
use futures_util::FutureExt;
use mass_codex::{Observation, ProviderRuntime};
use mass_runtime::{RuntimeEvents, SessionCommands};
use serde_json::json;
use std::{
    panic::AssertUnwindSafe,
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};
use tokio::sync::Mutex;

#[derive(Default)]
struct Facts {
    detached: Arc<AtomicUsize>,
    messages: usize,
}
impl RuntimeEvents for Facts {
    fn submit(&mut self, fact: mass_runtime::RuntimeFact) -> anyhow::Result<()> {
        if matches!(
            fact.event_type.as_str(),
            "user.message"
                | "agent.message"
                | "agent.thinking"
                | "agent.message_chunk"
                | "agent.thinking_chunk"
        ) {
            self.messages += 1;
        }
        Ok(())
    }
    fn detach(&mut self) {
        self.detached.fetch_add(1, Ordering::SeqCst);
    }
    fn disconnect(&mut self, _: &str) {
        self.detached.fetch_add(1, Ordering::SeqCst);
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
#[ignore = "real reviewed Codex binary required; run make test-native (no model calls in this case)"]
async fn shared_host_attachment_close_resume_and_stop_preserve_ownership() {
    let binary = std::env::var("MASS_TEST_CODEX").expect("MASS_TEST_CODEX required");
    let sha = std::env::var("MASS_TEST_CODEX_SHA256").expect("MASS_TEST_CODEX_SHA256 required");
    let base = std::env::var_os("MASS_TEST_EVIDENCE")
        .map(std::path::PathBuf::from)
        .unwrap_or_else(std::env::temp_dir);
    std::fs::create_dir_all(&base).unwrap();
    let root = tempfile::Builder::new()
        .prefix("mass-codex-lifecycle-")
        .tempdir_in(base)
        .unwrap()
        .keep();
    eprintln!("Codex module evidence: {}", root.display());
    let runtime = ProviderRuntime::new(
        serde_json::from_value(json!({"provider":"codex","executable":binary,"sha256":sha}))
            .unwrap(),
        Observation {
            directory: Some(root.join("native")),
            ..Default::default()
        },
    );
    let result = AssertUnwindSafe(async {
        let (one, two) = tokio::join!(runtime.start(), runtime.start());
        let host = one.unwrap();
        assert!(
            Arc::ptr_eq(&host, &two.unwrap()),
            "runtime activation must be single-flight"
        );
        let facts = Arc::new(Mutex::new(Facts::default()));
        let peer_facts = Arc::new(Mutex::new(Facts::default()));
        let deadline = || tokio::time::Instant::now() + Duration::from_secs(30);
        let (one, two) = tokio::join!(
            host.new_session(
                root.clone(),
                facts.clone(),
                uuid::Uuid::now_v7().to_string(),
                deadline(),
                Arc::new(())
            ),
            host.new_session(
                root.clone(),
                peer_facts.clone(),
                uuid::Uuid::now_v7().to_string(),
                deadline(),
                Arc::new(())
            )
        );
        let one = one.unwrap();
        let two = two.unwrap();
        one.commit_creation();
        two.commit_creation();
        assert_ne!(one.native_session_id(), two.native_session_id());
        assert_eq!(one.runtime()["pid"], two.runtime()["pid"]);
        one.cancel().await.unwrap();
        let old_commands = SessionCommands::new(one.clone());
        let id = one.native_session_id().to_owned();
        one.close().await.unwrap();
        assert!(!one.online());
        assert!(two.online());
        assert_eq!(facts.lock().await.detached.load(Ordering::SeqCst), 1);
        let resumed = host
            .resume_session(
                root.clone(),
                id.clone(),
                facts.clone(),
                uuid::Uuid::now_v7().to_string(),
                deadline(),
                Arc::new(()),
            )
            .await
            .unwrap();
        resumed.commit_creation();
        assert_eq!(resumed.native_session_id(), id);
        assert!(
            old_commands
                .prompt(vec![json!({"type":"text","text":"OLD_MUST_NOT_SEND"})])
                .await
                .is_err()
        );
        assert!(old_commands.cancel().await.is_err());
        assert!(resumed.online());
        assert!(two.online());
        assert_eq!(
            facts.lock().await.messages,
            0,
            "lifecycle must never manufacture message facts"
        );
        resumed.close().await.unwrap();
        two.close().await.unwrap();
    })
    .catch_unwind()
    .await;
    tokio::time::timeout(Duration::from_secs(40), runtime.stop())
        .await
        .expect("owned runtime stop deadline");
    assert!(runtime.start().await.is_err());
    if let Err(panic) = result {
        std::panic::resume_unwind(panic);
    }
    std::fs::write(root.join("result.json"),json!({"passed":true,"rustTest":"shared_host_attachment_close_resume_and_stop_preserve_ownership"}).to_string()).unwrap();
}
