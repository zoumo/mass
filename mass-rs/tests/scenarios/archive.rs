use crate::support::{Live, Native};
use futures_util::FutureExt;
use mass_runtime::SessionCommands;
use serde_json::{Value, json};
use std::{panic::AssertUnwindSafe, path::PathBuf, time::Duration};

struct Checkpoint {
    session: Value,
    preserved: Vec<Value>,
    token: String,
    journal: PathBuf,
    old: SessionCommands,
    old_live:
        std::sync::Mutex<tokio::sync::broadcast::Receiver<mass_events::handlers::ImmutableEvent>>,
}
async fn running(h: &Native, name: &str) {
    tokio::time::timeout(Duration::from_secs(150), async {
        loop {
            if h.result("session/status", name, json!({})).await["state"] == "running" {
                break;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
    })
    .await
    .expect("peer must be observed running");
}
fn spawn_count(h: &Native) -> usize {
    std::fs::read_dir(h.root.join("native"))
        .unwrap()
        .filter_map(Result::ok)
        .filter(|r| r.path().join("spawn.json").exists())
        .count()
}

pub async fn run(h: &Native, case: &str) {
    let session = h.new_session("target").await;
    let mut live = h.live("target").await;
    if case == "idle" {
        assert_eq!(
            h.result("session/archive", "target", json!({})).await["lifecycle"],
            "offline"
        );
        assert_eq!(
            h.result("session/archive", "target", json!({})).await["sessionId"],
            session["sessionId"]
        );
        save(h, case, json!({"session":session}));
        return;
    }
    if case == "failed-resume" {
        h.result("session/archive", "target", json!({})).await;
        h.shutdown().await;
        let metadata = h.session_dir(&session).join("metadata.json");
        let mut value: Value = serde_json::from_slice(&std::fs::read(&metadata).unwrap()).unwrap();
        value["recovery"]["sessionId"] = json!(uuid::Uuid::now_v7().to_string());
        std::fs::write(&metadata, value.to_string()).unwrap();
        let saved = std::fs::read(&metadata).unwrap();
        let restarted = h.reopen(Default::default(), json!({}));
        let result = AssertUnwindSafe(async {
            let rejected = restarted.call("session/resume", "target", json!({})).await;
            assert!(rejected.get("error").is_some());
            assert_eq!(std::fs::read(&metadata).unwrap(), saved);
            assert_eq!(
                restarted
                    .result("session/status", "target", json!({}))
                    .await["lifecycle"],
                "offline"
            );
            assert_eq!(restarted.frames("thread/start").len(), 1);
            assert_eq!(restarted.frames("thread/resume").len(), 0);
            save(&restarted, case, json!({"rejected":rejected}));
        })
        .catch_unwind()
        .await;
        restarted.shutdown().await;
        if let Err(panic) = result {
            std::panic::resume_unwind(panic);
        }
        return;
    }
    let token = format!("NATIVE05_{}", uuid::Uuid::now_v7().simple());
    h.prompt(
        "target",
        &format!(
            "Remember this private token for later: {token}. Reply only STORED. Do not use tools."
        ),
    )
    .await;
    h.idle("target").await;
    if matches!(case, "same-host" | "cold" | "old-commands" | "corrupt") {
        h.prompt("target","Use the shell tool to run exactly: printf 'TOOL_ONCE_05\\n' >> once.txt. Then reply RECORDED.").await;
        h.idle("target").await;
        assert_eq!(
            std::fs::read_to_string(h.root.join("target/once.txt")).unwrap(),
            "TOOL_ONCE_05\n"
        );
    }
    let preserved = h.history("target").await;
    h.assert_history("target").await;
    let old = h.captured_commands();
    let mut old_live = h
        .service
        .watch(json!({"sessionName":"target"}))
        .await
        .unwrap();
    assert!(preserved.iter().any(|e| e["type"] == "agent.message"));
    let peer = h
        .result(
            "session/new",
            "peer",
            json!({"spec":{"runtimeName":"codex","cwd":h.root.join("target")}}),
        )
        .await;
    assert_eq!(h.runtime(&peer)["pid"], h.runtime(&session)["pid"]);
    let peer_work=h.call("session/prompt","peer",json!({"prompt":[{"type":"text","text":"Do not use tools. Write 50 short numbered sentences about mountains. End with PEER_CONTINUES_05."}]}));
    let closing = async {
        running(h, "peer").await;
        let close = async {
            let archived = h.call("session/archive", "target", json!({}));
            let observe = async {
                loop {
                    if h.result("session/status", "target", json!({})).await["lifecycle"]
                        != "online"
                    {
                        break;
                    }
                    tokio::time::sleep(Duration::from_millis(20)).await;
                }
                if case == "archive-host-loss" {
                    h.wait(|| {
                        h.frames("thread/closed")
                            .iter()
                            .any(|r| r["params"]["threadId"] == session["sessionId"])
                            .then_some(())
                    })
                    .await;
                    h.kill_host("target").await;
                }
            };
            let (archived, ()) = tokio::join!(archived, observe);
            archived
        };
        if matches!(case, "active" | "approval" | "queued") {
            let before = h
                .frames("item/agentMessage/delta")
                .iter()
                .filter(|r| r["params"]["threadId"] == session["sessionId"])
                .count();
            let text = match case {
                "queued" => "QUEUED_ARCHIVE_MUST_NOT_REACH_NATIVE",
                "approval" => {
                    "Use the shell tool to run exactly: printf NEVER > forbidden.txt. Then reply DONE."
                }
                _ => {
                    "Do not use tools. Write 200 numbered long sentences about trees, one sentence per line."
                }
            };
            let pending = h.call(
                "session/prompt",
                "target",
                json!({"prompt":[{"type":"text","text":text}]}),
            );
            let action = async {
                if case == "queued" {
                    tokio::time::sleep(Duration::from_millis(100)).await;
                } else if case == "approval" {
                    h.wait(|| {
                        h.frames("item/commandExecution/requestApproval")
                            .iter()
                            .any(|r| r["params"]["threadId"] == session["sessionId"])
                            .then_some(())
                    })
                    .await;
                } else {
                    h.wait(|| {
                        (h.frames("item/agentMessage/delta")
                            .iter()
                            .filter(|r| r["params"]["threadId"] == session["sessionId"])
                            .count()
                            > before)
                            .then_some(())
                    })
                    .await;
                }
                close.await
            };
            let (receipt, archived) = tokio::join!(pending, action);
            if case == "queued" {
                assert_eq!(receipt["error"]["data"]["kind"], "cancelled");
            }
            archived
        } else {
            close.await
        }
    };
    let (peer_receipt, archived) = tokio::join!(peer_work, closing);
    if case != "archive-host-loss" {
        assert!(peer_receipt.get("result").is_some(), "{peer_receipt}");
    }
    if matches!(case, "late-closed" | "configured-deadline") {
        assert_eq!(archived["error"]["data"]["kind"], "close_unknown");
        assert_eq!(
            h.call("session/resume", "target", json!({})).await["error"]["data"]["kind"],
            "close_unknown"
        );
        assert_eq!(
            h.result("session/status", "target", json!({})).await["lifecycle"],
            "closing"
        );
        let repeated = h.call("session/archive", "target", json!({})).await;
        assert_ne!(
            repeated["error"]["data"]["operationId"],
            archived["error"]["data"]["operationId"]
        );
        let mut repeated_error = repeated["error"].clone();
        let mut original_error = archived["error"].clone();
        assert!(
            repeated_error["data"]
                .as_object_mut()
                .unwrap()
                .remove("operationId")
                .unwrap()
                .is_string()
        );
        assert!(
            original_error["data"]
                .as_object_mut()
                .unwrap()
                .remove("operationId")
                .unwrap()
                .is_string()
        );
        assert_eq!(repeated_error, original_error);
        assert!(
            h.call(
                "session/prompt",
                "target",
                json!({"prompt":[{"type":"text","text":"CLOSING_MUST_NOT_REACH_NATIVE"}]})
            )
            .await
            .get("error")
            .is_some()
        );
        h.released("target").await;
    } else {
        assert_eq!(archived["result"]["lifecycle"], "offline", "{archived}");
    }
    if matches!(case, "active" | "approval") {
        let interrupts: Vec<_> = h
            .frames("turn/interrupt")
            .into_iter()
            .filter(|r| r["params"]["threadId"] == session["sessionId"])
            .collect();
        assert_eq!(interrupts.len(), 1);
        assert!(
            h.frames("turn/completed")
                .iter()
                .any(|r| r["params"]["threadId"] == session["sessionId"]
                    && r["params"]["turn"]["id"] == interrupts[0]["params"]["turnId"]
                    && r["params"]["turn"]["status"] == "interrupted")
        );
    }
    assert!(
        !h.frames("turn/interrupt")
            .iter()
            .any(|r| r["params"]["threadId"] == peer["sessionId"])
    );
    if case == "archive-host-loss" {
        h.released("peer").await;
    } else {
        assert_eq!(
            h.runtime(&h.result("session/status", "peer", json!({})).await)["pid"],
            h.runtime(&session)["pid"]
        );
        h.idle("peer").await;
        assert!(
            h.frames("item/completed")
                .iter()
                .any(|r| r["params"]["threadId"] == peer["sessionId"]
                    && r["params"]["item"]["type"] == "agentMessage")
        );
    }
    let settled = h.history("target").await;
    assert_eq!(settled[..preserved.len()], preserved);
    assert!(!h.root.join("target/forbidden.txt").exists());
    let journal = h.session_dir(&session).join("events.jsonl");
    while old_live.try_recv().is_ok() {}
    let checkpoint = Checkpoint {
        session,
        preserved: settled,
        token,
        journal,
        old,
        old_live: std::sync::Mutex::new(old_live),
    };
    let restarted = if matches!(case, "cold" | "corrupt" | "provider-binding") {
        h.shutdown().await;
        if case == "provider-binding" {
            use std::io::Write;
            std::fs::OpenOptions::new()
                .append(true)
                .open(&checkpoint.journal)
                .unwrap()
                .write_all(b"PARTIAL")
                .unwrap();
        }
        if case == "provider-binding" {
            let path = checkpoint.journal.with_file_name("metadata.json");
            let mut metadata: Value =
                serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
            metadata["recovery"]["runtime"]["provider"] = json!("claude");
            std::fs::write(path, serde_json::to_vec(&metadata).unwrap()).unwrap();
        }
        Some(h.reopen(Default::default(), json!({})))
    } else {
        None
    };
    let active = restarted.as_ref().unwrap_or(h);
    let result = AssertUnwindSafe(resume(active, case, &checkpoint, &mut live))
        .catch_unwind()
        .await;
    if let Some(restarted) = restarted {
        restarted.shutdown().await;
    }
    if let Err(panic) = result {
        std::panic::resume_unwind(panic);
    }
}
async fn resume(h: &Native, case: &str, c: &Checkpoint, live: &mut Live) {
    if case == "provider-binding" {
        let saved = std::fs::read(&c.journal).unwrap();
        assert!(
            h.call("session/resume", "target", json!({}))
                .await
                .get("error")
                .is_some()
        );
        assert_eq!(std::fs::read(&c.journal).unwrap(), saved);
        assert_eq!(spawn_count(h), 1);
        save(h, case, json!({}));
        return;
    }
    if matches!(case, "cold" | "corrupt") {
        assert_eq!(h.history("target").await, c.preserved);
        assert_eq!(spawn_count(h), 1);
    }
    let original = std::fs::read(&c.journal).unwrap();
    if case == "tail" {
        use std::io::Write;
        std::fs::OpenOptions::new()
            .append(true)
            .open(&c.journal)
            .unwrap()
            .write_all(b"{\"uncommitted\":true}")
            .unwrap();
    }
    if case == "corrupt" {
        corrupt(h, c, &original).await;
    }
    let resumed = h.result("session/resume", "target", json!({})).await;
    assert_eq!(resumed["sessionId"], c.session["sessionId"]);
    assert_eq!(
        h.runtime(&resumed)["pid"] != h.runtime(&c.session)["pid"],
        matches!(case, "cold" | "corrupt" | "archive-host-loss")
    );
    assert_eq!(std::fs::read(&c.journal).unwrap(), original);
    assert_eq!(h.history("target").await, c.preserved);
    h.reconnect(live, "target").await;
    if case == "old-commands" {
        assert!(
            c.old
                .prompt(vec![
                    json!({"type":"text","text":"OLD_ATTACHMENT_MUST_NOT_REACH_NATIVE"})
                ])
                .await
                .is_err()
        );
        assert!(c.old.cancel().await.is_err());
        assert!(c.old.set_config_option("model", "stale").await.is_err());
    }
    h.prompt(
        "target",
        "Without tools, reply only with the private token I asked you to remember earlier.",
    )
    .await;
    h.idle("target").await;
    let recovered = h.history("target").await;
    assert_eq!(recovered[..c.preserved.len()], c.preserved);
    assert!(
        recovered[c.preserved.len()..]
            .iter()
            .any(|e| e["type"] == "agent.message" && e["payload"].to_string().contains(&c.token))
    );
    if h.root.join("target/once.txt").exists() {
        assert_eq!(
            std::fs::read_to_string(h.root.join("target/once.txt")).unwrap(),
            "TOOL_ONCE_05\n"
        );
    }
    h.assert_history("target").await;
    h.wait(|| {
        (h.handler_rows()
            .iter()
            .filter(|row| row["sessionName"] == "target" && row.get("seq").is_some())
            .cloned()
            .collect::<Vec<_>>()
            == recovered)
            .then_some(())
    })
    .await;
    assert!(
        matches!(
            c.old_live.lock().unwrap().try_recv(),
            Err(tokio::sync::broadcast::error::TryRecvError::Empty
                | tokio::sync::broadcast::error::TryRecvError::Closed)
        ),
        "old subscriber must not follow the new Attachment"
    );
    h.wait(|| {
        (live
            .rows()
            .iter()
            .filter(|e| e.get("seq").is_some())
            .cloned()
            .collect::<Vec<_>>()
            == recovered)
            .then_some(())
    })
    .await;
    assert!(h.frames("thread/archive").is_empty());
    assert!(h.frames("thread/delete").is_empty());
    for forbidden in [
        "QUEUED_ARCHIVE_MUST_NOT_REACH_NATIVE",
        "OLD_ATTACHMENT_MUST_NOT_REACH_NATIVE",
        "CLOSING_MUST_NOT_REACH_NATIVE",
    ] {
        assert!(!h.wire().to_string().contains(forbidden));
    }
    save(
        h,
        case,
        json!({"session":c.session,"resumed":resumed,"historyCount":recovered.len(),"preservedCount":c.preserved.len()}),
    );
}
async fn corrupt(h: &Native, c: &Checkpoint, original: &[u8]) {
    let variants = [
        (
            "private-version",
            "user.message",
            "/evidence/version",
            json!(99),
        ),
        (
            "private-adapter",
            "user.message",
            "/evidence/adapter",
            json!("foreign"),
        ),
        (
            "content-block",
            "user.message",
            "/payload/content",
            json!([{"type":"text","text":42}]),
        ),
        (
            "provider",
            "user.message",
            "/payload/source/provider",
            json!("claude"),
        ),
        (
            "native-id",
            "user.message",
            "/payload/source/nativeSessionId",
            json!("wrong-native"),
        ),
        (
            "payload-id",
            "user.message",
            "/payload/messageId",
            json!("other"),
        ),
        ("public-id", "user.message", "/itemId", json!("user_00")),
        (
            "native-payload",
            "user.message",
            "/payload/nativeItem/id",
            json!("other"),
        ),
        ("seq", "user.message", "/seq", json!(2)),
        ("session", "user.message", "/sessionId", json!("other")),
        ("schema", "user.message", "/schemaVersion", json!(99)),
        (
            "source-connection",
            "user.message",
            "/payload/source/connectionId",
            json!("invalid"),
        ),
        (
            "source-host",
            "user.message",
            "/payload/source/hostGeneration",
            json!("invalid"),
        ),
        (
            "source-evidence",
            "user.message",
            "/payload/source/completionEvidence",
            json!("idle"),
        ),
        (
            "content-projection",
            "user.message",
            "/payload/content",
            json!([{"type":"text","text":"changed"}]),
        ),
        (
            "agent-native-text-missing",
            "agent.message",
            "/payload/nativeItem/text",
            Value::Null,
        ),
        (
            "agent-native-text-type",
            "agent.message",
            "/payload/nativeItem/text",
            json!(42),
        ),
        (
            "tool-native-output",
            "agent.tool_result",
            "/payload/nativeResult/aggregatedOutput",
            json!(42),
        ),
        (
            "tool-raw-input",
            "agent.tool_result",
            "/payload/rawInput/command",
            json!("other"),
        ),
        (
            "tool-call-context",
            "agent.tool_result",
            "/payload/callContext/nativeItem/command",
            Value::Null,
        ),
        (
            "tool-call-context-native-id",
            "agent.tool_result",
            "/payload/callContext/nativeItem/id",
            json!("other"),
        ),
    ];
    let mut rejected = Vec::new();
    for (label, kind, pointer, value) in variants {
        let mut records: Vec<Value> = original
            .split(|b| *b == b'\n')
            .filter(|line| !line.is_empty())
            .map(|line| serde_json::from_slice(line).unwrap())
            .collect();
        let event = records
            .iter_mut()
            .find(|e| e["event"]["type"] == kind)
            .unwrap();
        if label == "agent-native-text-missing" {
            event["evidence"]["payload"]["nativeItem"]
                .as_object_mut()
                .unwrap()
                .remove("text");
        } else if label == "tool-call-context" {
            event["evidence"]["payload"]["callContext"]["nativeItem"]
                .as_object_mut()
                .unwrap()
                .remove("command");
        } else {
            let pointer = if pointer.starts_with("/evidence/") {
                pointer.to_owned()
            } else if pointer.starts_with("/payload/source/")
                || pointer.starts_with("/payload/native")
                || pointer.starts_with("/payload/callContext/native")
            {
                format!("/evidence{pointer}")
            } else {
                format!("/event{pointer}")
            };
            *event.pointer_mut(&pointer).unwrap() = value;
        }
        let damaged = format!(
            "{}\nPARTIAL",
            records
                .iter()
                .map(Value::to_string)
                .collect::<Vec<_>>()
                .join("\n")
        );
        std::fs::write(&c.journal, &damaged).unwrap();
        let before = h.wire();
        let response = h.call("session/resume", "target", json!({})).await;
        assert!(response.get("error").is_some(), "{label}: {response}");
        assert_eq!(std::fs::read_to_string(&c.journal).unwrap(), damaged);
        assert_eq!(
            h.wire(),
            before,
            "invalid history must reject before native attach"
        );
        rejected.push(json!({"case":label,"response":response}));
    }
    std::fs::write(&c.journal, original).unwrap();
    let metadata = c.journal.with_file_name("metadata.json");
    let bytes = std::fs::read(&metadata).unwrap();
    for (field, value) in [
        ("formatVersion", json!(1)),
        ("provider", json!("claude")),
        ("nativeSessionId", json!("other")),
    ] {
        let mut bad: Value = serde_json::from_slice(&bytes).unwrap();
        bad[field] = value;
        std::fs::write(&metadata, bad.to_string()).unwrap();
        assert!(
            h.call("session/resume", "target", json!({}))
                .await
                .get("error")
                .is_some()
        );
        assert_eq!(
            serde_json::from_slice::<Value>(&std::fs::read(&metadata).unwrap()).unwrap(),
            bad
        );
        assert_eq!(std::fs::read(&c.journal).unwrap(), original);
        rejected.push(json!({"case":format!("metadata-{field}")}));
    }
    std::fs::write(metadata, bytes).unwrap();
    assert_eq!(spawn_count(h), 1);
    std::fs::write(
        h.root.join("corruption-rejections.json"),
        serde_json::to_vec_pretty(&rejected).unwrap(),
    )
    .unwrap();
}
fn save(h: &Native, case: &str, detail: Value) {
    std::fs::write(
        h.root.join("result.json"),
        json!({"passed":true,"migratedFrom":"codex_archive_e2e.py","case":case,"detail":detail})
            .to_string(),
    )
    .unwrap();
}
