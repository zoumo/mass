//! Explicit real Codex acceptance. Never run implicitly in the offline suite.
mod scenarios;
mod support;
use futures_util::FutureExt;
use serde_json::json;
use std::{panic::AssertUnwindSafe, time::Duration};
use support::Native;

macro_rules! native_test {
    ($name:ident, $observation:expr, $config:expr, |$h:ident| $body:block) => {
        #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
        #[ignore = "explicit native acceptance; run make test-native with the reviewed Codex identity"]
        async fn $name() {
            let $h = Native::configured(stringify!($name), $observation, $config);
            let result = AssertUnwindSafe(async $body).catch_unwind().await;
            $h.shutdown().await;
            if let Err(panic) = result { std::panic::resume_unwind(panic); }
            let path=$h.root.join("result.json");
            let mut report:serde_json::Value=std::fs::read(&path).ok().map(|bytes|serde_json::from_slice(&bytes).unwrap()).unwrap_or_else(||json!({}));
            report["passed"]=json!(true);report["rustTest"]=json!(stringify!($name));
            std::fs::write(path,report.to_string()).unwrap();
        }
    };
    ($name:ident, $observation:expr, |$h:ident| $body:block) => {
        #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
        #[ignore = "requires MASS_TEST_CODEX and MASS_TEST_CODEX_SHA256, existing auth and real model calls; run make test-native"]
        async fn $name() {
            let $h = Native::new(stringify!($name), $observation);
            let result = AssertUnwindSafe(async $body).catch_unwind().await;
            $h.shutdown().await;
            if let Err(panic) = result { std::panic::resume_unwind(panic); }
            let path=$h.root.join("result.json");
            let mut report:serde_json::Value=std::fs::read(&path).ok().map(|bytes|serde_json::from_slice(&bytes).unwrap()).unwrap_or_else(||json!({}));
            report["passed"]=json!(true);report["rustTest"]=json!(stringify!($name));
            std::fs::write(path,report.to_string()).unwrap();
        }
    };
}

macro_rules! cancel_test {
    ($name:ident,$case:literal,$observation:expr) => {
        native_test!($name, $observation, |h| {
            scenarios::cancel::run(&h, $case).await;
        });
    };
}
cancel_test!(migrated_cancel_running, "running", Default::default());
cancel_test!(migrated_cancel_stream, "stream", Default::default());
cancel_test!(
    migrated_cancel_queued,
    "queued",
    mass_codex::Observation {
        command_write_delay: Duration::from_secs(5),
        ..Default::default()
    }
);
cancel_test!(
    migrated_cancel_approval,
    "approval",
    mass_codex::Observation {
        approval_response_delay: Duration::from_secs(20),
        ..Default::default()
    }
);
cancel_test!(
    migrated_cancel_acceptance,
    "acceptance",
    mass_codex::Observation {
        start_response_delay: Duration::from_secs(8),
        ..Default::default()
    }
);
cancel_test!(
    migrated_cancel_ack_late,
    "ack-late",
    mass_codex::Observation {
        interrupt_response_delay: Duration::from_secs(5),
        ..Default::default()
    }
);
cancel_test!(
    migrated_cancel_terminal_late,
    "terminal-late",
    mass_codex::Observation {
        terminal_delay: Duration::from_secs(5),
        ..Default::default()
    }
);
cancel_test!(
    migrated_cancel_unknown,
    "unknown",
    mass_codex::Observation {
        interrupt_response_delay: Duration::from_secs(35),
        ..Default::default()
    }
);
cancel_test!(
    migrated_cancel_late_turn,
    "late-turn",
    mass_codex::Observation {
        start_response_delay: Duration::from_secs(35),
        event_delivery_delay: Duration::from_secs(35),
        ..Default::default()
    }
);
cancel_test!(
    migrated_cancel_disconnect,
    "disconnect",
    mass_codex::Observation {
        disconnect_interrupt: true,
        ..Default::default()
    }
);

native_test!(
    migrated_create_version,
    Default::default(),
    scenarios::creation::version_config(),
    |h| {
        scenarios::creation::single(&h, "version").await;
    }
);
native_test!(
    migrated_create_metadata,
    mass_codex::Observation {
        thread_response_delay: Duration::from_secs(2),
        thread_response_delay_cwd: Some("target".into()),
        thread_request_delay: Duration::from_secs(1),
        ..Default::default()
    },
    |h| {
        scenarios::creation::single(&h, "metadata").await;
    }
);
native_test!(
    migrated_create_open_timeout,
    mass_codex::Observation {
        thread_response_delay: Duration::from_secs(31),
        ..Default::default()
    },
    |h| {
        scenarios::creation::single(&h, "open_timeout").await;
    }
);
native_test!(
    migrated_create_shared_open,
    mass_codex::Observation {
        thread_request_delay: Duration::from_secs(1),
        ..Default::default()
    },
    |h| {
        scenarios::creation::shared(&h, "open").await;
    }
);
native_test!(
    migrated_create_shared_metadata,
    mass_codex::Observation {
        thread_response_delay: Duration::from_secs(2),
        thread_response_delay_cwd: Some("provisional".into()),
        thread_request_delay: Duration::from_secs(1),
        ..Default::default()
    },
    |h| {
        scenarios::creation::shared(&h, "metadata").await;
    }
);
native_test!(
    migrated_create_shared_late,
    mass_codex::Observation {
        thread_request_delay: Duration::from_secs(1),
        thread_response_delay: Duration::from_secs(31),
        thread_response_delay_cwd: Some("provisional".into()),
        ..Default::default()
    },
    |h| {
        scenarios::creation::shared(&h, "late").await;
    }
);

native_test!(
    migrated_fault_slow,
    mass_codex::Observation {
        core_write_delay: Duration::from_secs(8),
        core_write_delay_thread: Some("target".into()),
        ..Default::default()
    },
    json!({}),
    |h| {
        scenarios::faults::run(&h, "slow").await;
    }
);

native_test!(
    migrated_fault_long_output,
    mass_codex::Observation {
        core_write_delay: Duration::from_secs(8),
        core_write_delay_thread: Some("target".into()),
        ..Default::default()
    },
    json!({}),
    |h| {
        scenarios::faults::run(&h, "long-output").await;
    }
);

native_test!(migrated_fault_append, Default::default(), json!({}), |h| {
    scenarios::faults::run(&h, "append").await;
});

native_test!(
    migrated_fault_backpressure,
    mass_codex::Observation {
        core_write_delay: Duration::from_secs(30),
        core_write_delay_thread: Some("target".into()),
        ..Default::default()
    },
    json!({"factLaneCapacity":3}),
    |h| {
        scenarios::faults::run(&h, "backpressure").await;
    }
);

native_test!(
    migrated_fault_sdk_oversize,
    Default::default(),
    json!({"sdkEventBytes":65536}),
    |h| {
        scenarios::faults::run(&h, "sdk-oversize").await;
    }
);

native_test!(
    migrated_fault_stop_stream,
    Default::default(),
    json!({}),
    |h| {
        scenarios::faults::run(&h, "stop-stream").await;
    }
);

native_test!(
    migrated_fault_stop_drain,
    mass_codex::Observation {
        core_write_delay: Duration::from_secs(8),
        core_write_delay_thread: Some("target".into()),
        ..Default::default()
    },
    json!({}),
    |h| {
        scenarios::faults::run(&h, "stop-drain").await;
    }
);

native_test!(
    migrated_fault_host_loss,
    mass_codex::Observation {
        approval_response_delay: Duration::from_secs(60),
        command_write_delay: Duration::from_secs(2),
        ..Default::default()
    },
    json!({}),
    |h| {
        scenarios::faults::run(&h, "host-loss").await;
    }
);

async fn tools_case(
    case: &str,
    observation: mass_codex::Observation,
    truncated: bool,
    delayed: bool,
) {
    let h = Native::with_shell_fixture(case, observation);
    let result = AssertUnwindSafe(scenarios::tools::run(&h, truncated, delayed))
        .catch_unwind()
        .await;
    h.shutdown().await;
    if let Err(panic) = result {
        std::panic::resume_unwind(panic);
    }
    let path = h.root.join("result.json");
    let mut report: serde_json::Value =
        serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
    report["rustTest"] = json!(case);
    std::fs::write(path, report.to_string()).unwrap();
}
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
#[ignore = "explicit native acceptance; reviewed Codex, existing auth and real model required"]
async fn migrated_tools() {
    tools_case("migrated_tools", Default::default(), false, false).await;
}
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
#[ignore = "explicit native acceptance; reviewed Codex, existing auth and real model required"]
async fn migrated_tools_delayed() {
    tools_case(
        "migrated_tools_delayed",
        mass_codex::Observation {
            approval_response_delay: Duration::from_secs(10),
            ..Default::default()
        },
        false,
        true,
    )
    .await;
}
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
#[ignore = "explicit native acceptance; reviewed Codex, existing auth and real model required"]
async fn migrated_tools_truncation() {
    tools_case("migrated_tools_truncation", Default::default(), true, false).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
#[ignore = "real Codex Handler failure/overflow/reentry matrix; run make test-native"]
async fn migrated_fault_handlers() {
    let h = Native::with_handlers(
        "migrated_fault_handlers",
        Default::default(),
        json!({}),
        scenarios::handlers::registrations,
    );
    let result = AssertUnwindSafe(scenarios::handlers::run(&h))
        .catch_unwind()
        .await;
    h.shutdown().await;
    if let Err(panic) = result {
        std::panic::resume_unwind(panic);
    }
}

native_test!(migrated_archive_idle, Default::default(), json!({}), |h| {
    scenarios::archive::run(&h, "idle").await;
});

native_test!(
    migrated_archive_same_host,
    Default::default(),
    json!({}),
    |h| {
        scenarios::archive::run(&h, "same-host").await;
    }
);

native_test!(migrated_archive_cold, Default::default(), json!({}), |h| {
    scenarios::archive::run(&h, "cold").await;
});

native_test!(
    migrated_archive_corrupt,
    Default::default(),
    json!({}),
    |h| {
        scenarios::archive::run(&h, "corrupt").await;
    }
);

native_test!(migrated_archive_tail, Default::default(), json!({}), |h| {
    scenarios::archive::run(&h, "tail").await;
});

native_test!(
    migrated_archive_active,
    Default::default(),
    json!({}),
    |h| {
        scenarios::archive::run(&h, "active").await;
    }
);

native_test!(
    migrated_archive_approval,
    mass_codex::Observation {
        approval_response_delay: Duration::from_secs(6),
        ..Default::default()
    },
    json!({}),
    |h| {
        scenarios::archive::run(&h, "approval").await;
    }
);

native_test!(
    migrated_archive_queued,
    mass_codex::Observation {
        command_write_delay: Duration::from_secs(3),
        ..Default::default()
    },
    json!({}),
    |h| {
        scenarios::archive::run(&h, "queued").await;
    }
);

native_test!(
    migrated_archive_late_closed,
    mass_codex::Observation {
        closed_delivery_delay: Duration::from_secs(35),
        ..Default::default()
    },
    json!({}),
    |h| {
        scenarios::archive::run(&h, "late-closed").await;
    }
);

native_test!(
    migrated_archive_old_commands,
    Default::default(),
    json!({}),
    |h| {
        scenarios::archive::run(&h, "old-commands").await;
    }
);

native_test!(
    migrated_archive_configured_deadline,
    mass_codex::Observation {
        closed_delivery_delay: Duration::from_secs(5),
        ..Default::default()
    },
    json!({"closeTimeoutMs":1000}),
    |h| {
        scenarios::archive::run(&h, "configured-deadline").await;
    }
);

native_test!(
    migrated_archive_failed_resume,
    Default::default(),
    json!({}),
    |h| {
        scenarios::archive::run(&h, "failed-resume").await;
    }
);

native_test!(
    migrated_archive_provider_binding,
    Default::default(),
    json!({}),
    |h| {
        scenarios::archive::run(&h, "provider-binding").await;
    }
);

native_test!(
    migrated_archive_host_loss,
    mass_codex::Observation {
        closed_delivery_delay: Duration::from_secs(35),
        ..Default::default()
    },
    json!({}),
    |h| {
        scenarios::archive::run(&h, "archive-host-loss").await;
    }
);

native_test!(migrated_text, Default::default(), |h| {
    scenarios::text::run(&h, false).await;
});
native_test!(
    migrated_text_full_only,
    mass_codex::Observation {
        drop_previews: true,
        start_response_delay: Duration::from_millis(1500),
        ..Default::default()
    },
    |h| {
        scenarios::text::run(&h, true).await;
    }
);
native_test!(
    migrated_input_unknown,
    mass_codex::Observation {
        start_response_delay: Duration::from_secs(35),
        ..Default::default()
    },
    |h| {
        scenarios::text::uncertainty(&h, true).await;
    }
);
native_test!(
    migrated_input_caller_gone,
    mass_codex::Observation {
        start_response_delay: Duration::from_secs(8),
        ..Default::default()
    },
    |h| {
        scenarios::text::uncertainty(&h, false).await;
    }
);

native_test!(
    migrated_shared,
    mass_codex::Observation {
        thread_response_delay: Duration::from_millis(1500),
        ..Default::default()
    },
    |h| {
        scenarios::shared::run(&h).await;
    }
);

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
#[ignore = "real test-owned WebSocket app-server and model calls; make test-native"]
async fn migrated_remote_operations() {
    scenarios::remote::run(false).await;
}
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
#[ignore = "real test-owned WebSocket app-server and model calls; make test-native"]
async fn migrated_remote_disconnect() {
    scenarios::remote::run(true).await;
}
