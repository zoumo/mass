//! Synthetic regression for interrupted -> late item/started ordering.
use super::*;
struct Facts(Arc<StdMutex<Vec<Value>>>);
impl RuntimeEvents for Facts {
    fn submit(&mut self, fact: mass_runtime::RuntimeFact) -> Result<()> {
        if fact.event_type.starts_with("agent.tool_") {
            self.0.lock().unwrap().push(
                json!({"result":fact.event_type == "agent.tool_result","payload":fact.payload,"evidence":fact.evidence}),
            );
        }
        Ok(())
    }
    fn detach(&mut self) {}
    fn disconnect(&mut self, _: &str) {}
}
#[tokio::test]
async fn interrupted_work_retains_ownership_of_a_late_tool_fact_without_granting_approval() {
    let trace: Vec<Value> = serde_json::from_str(include_str!(
        "../tests/fixtures/late-tool-after-terminal.json"
    ))
    .unwrap();
    let (tx, _requests) = mpsc::channel(32);
    let (control, _controls) = mpsc::channel(32);
    let host = Arc::new(Host {
        tx,
        control,
        control_capacity: Arc::new(Semaphore::new(32)),
        command_capacity: Arc::new(Semaphore::new(32)),
        attachment_capacity: Arc::new(Semaphore::new(128)),
        unresolved_opens: StdMutex::new(vec![]),
        context_capacity: Arc::new(Semaphore::new(MAX_CONTEXT * 4)),
        routes: Arc::new(StdMutex::new(BTreeMap::new())),
        online: Arc::new(AtomicBool::new(true)),
        accepting: AtomicBool::new(true),
        recovery_scope: None,
        owns_process: true,
        identity: json!({}),
        generation: "host".into(),
        observation: Observation::default(),
        recorder: Arc::new(Recorder(None, vec![])),
        fact_lane_capacity: 64,
        sdk_event_bytes: MAX_CONTEXT,
        close_timeout: DEADLINE,
        initialized: AtomicBool::new(true),
        initialization_error: StdMutex::new(None),
        ready: Notify::new(),
        stopping: AtomicBool::new(false),
        stop: Notify::new(),
        stopped: AtomicBool::new(false),
        local_resources_ended: AtomicBool::new(false),
        done: Notify::new(),
    });
    let facts = Arc::new(StdMutex::new(vec![]));
    let captured = facts.clone();
    let handle = host
        .bind(
            json!({"thread":{"id":trace[0]["params"]["threadId"]}}),
            Arc::new(move |_| Arc::new(Mutex::new(Facts(captured.clone())))),
            "attachment".into(),
            Arc::new(()),
            host.attachment_capacity
                .clone()
                .acquire_owned()
                .await
                .unwrap(),
        )
        .await
        .unwrap();
    {
        let mut state = handle.state.lock().unwrap();
        state.submission = Some(Submission {
            id: "submission".into(),
            operation_id: "operation".into(),
            rejected: false,
            sent: Arc::new(AtomicBool::new(true)),
            accepted: true,
            recorded: Some("user-fact".into()),
            turn: trace[1]["params"]["turn"]["id"].as_str().map(str::to_owned),
            terminal: false,
            start_settled: true,
            cancelled_unsent: false,
            cancelled: true,
            acceptance_done: true,
            observer_done: true,
        });
        state.cancel_generation = 1;
        state.cancellation = Some(Arc::new(Cancellation {
            generation: 1,
            submission_id: Some("submission".into()),
            result: watch::channel(None).0,
            acknowledged: AtomicBool::new(false),
            failed: AtomicBool::new(false),
            native_settled: AtomicBool::new(false),
        }));
    }
    handle.observe(&trace[0]).await.unwrap();
    handle.observe(&trace[1]).await.unwrap();
    let observed = handle.observe(&trace[2]).await;
    assert!(
        observed.is_ok(),
        "same-work late fact must not disconnect the attachment: {observed:?}"
    );
    assert!(handle.online());
    let params = &trace[2]["params"];
    assert!(!handle.approval_valid(
        params["turnId"].as_str().unwrap(),
        params["item"]["id"].as_str().unwrap()
    ));
    let blocked = handle
        .clone()
        .begin(InputCommand {
            operation_id: "blocked".into(),
            blocks: vec![InputBlock::Text {
                text: "new work".into(),
            }],
        })
        .unwrap_err();
    assert_eq!(blocked.kind, CommandErrorKind::Cancelling);
    let mut unowned = trace[2].clone();
    unowned["params"]["turnId"] = json!("another-work");
    assert!(handle.observe(&unowned).await.is_err());
    let mut changed_input = trace[2].clone();
    changed_input["params"]["item"]["command"] = json!("different command");
    assert!(handle.observe(&changed_input).await.is_err());
    // A duplicate remains a fact of the original work after a subsequent work is selected.
    handle.state.lock().unwrap().tools.clear();
    handle
        .state
        .lock()
        .unwrap()
        .submission
        .as_mut()
        .unwrap()
        .turn = Some("new-work".into());
    handle.observe(&trace[2]).await.unwrap();
    handle.events.seal(None);
    handle.events.drain().await;
    let facts = facts.lock().unwrap();
    assert_eq!(facts.len(), 1);
    assert_eq!(facts[0]["result"], false);
    assert!(facts[0]["payload"].get("nativeItem").is_none());
    assert_eq!(
        facts[0]["evidence"]["payload"]["nativeItem"],
        trace[0]["params"]["item"]
    );
    host.routes.lock().unwrap().clear();
}

#[test]
fn native_user_text_elements_remain_private_while_the_complete_public_text_is_preserved() {
    let notification: Value = serde_json::from_str(include_str!(
        "../tests/fixtures/user-message-text-elements.json"
    ))
    .unwrap();
    let item = &notification["params"]["item"];
    let payload = json!({"nativeItem":item,"source":{},"content":item["content"]});
    let fact = normalize_fact("user.message", "user", "stable-key", payload, false).unwrap();
    assert_eq!(fact.evidence["payload"]["nativeItem"], *item);
    assert_eq!(
        fact.payload["content"],
        json!([{"type":"text","text":item["content"][0]["text"]}])
    );
}

#[test]
fn remote_credential_echo_is_redacted_even_when_its_reference_name_has_no_secret_marker() {
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("wire.jsonl");
    let recorder = Recorder(
        Some(StdMutex::new(std::fs::File::create(&path).unwrap())),
        vec!["dummy-auth-source-canary".into()],
    );
    recorder.record("server",&json!({"error":{"message":"upstream echo dummy-auth-source-canary"},"nested":["dummy-auth-source-canary"]})).unwrap();
    let recorded = std::fs::read_to_string(path).unwrap();
    assert!(!recorded.contains("dummy-auth-source-canary"));
    assert!(recorded.contains("[REDACTED]"));
}
