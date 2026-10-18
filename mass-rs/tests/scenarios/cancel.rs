use crate::support::Native;
use serde_json::json;
use std::{
    collections::BTreeSet,
    time::{Duration, Instant},
};

pub async fn run(h: &Native, case: &str) {
    let session = h.new_session("target").await;
    let peer = if case == "approval" {
        Some(h.new_session("peer").await)
    } else {
        None
    };
    if let Some(peer) = &peer {
        assert_eq!(h.runtime(peer)["pid"], h.runtime(&session)["pid"]);
    }
    let live = h.live("target").await;
    if !matches!(case, "queued" | "acceptance" | "late-turn") {
        h.prompt("target", "Without tools reply only MASS_PRESERVED_04.")
            .await;
        h.idle("target").await;
    }
    let preserved = h.history("target").await;
    let starts = h.frames("turn/start").len();
    let approvals = h.frames("item/commandExecution/requestApproval").len();
    let previews = live
        .rows()
        .iter()
        .filter(|e| e["type"] == "agent.message_chunk")
        .count();
    let text = match case {
        "approval" => {
            "Use only the shell execution tool. Execute exactly: printf 'FORBIDDEN_04' > forbidden.txt . Then reply done. Actually execute it."
        }
        "stream" => {
            "Without any tools, output a very long numbered list of 1000 unique sentences about trees. Start immediately and do not abbreviate."
        }
        _ => {
            "Use only the shell execution tool. Execute exactly this single shell call in the session cwd: echo $$ > running.pid; printf 'RUNNING_04' > running.marker; sleep 90; printf 'FORBIDDEN_04' > forbidden.txt . Wait for the command to finish. Then reply done. Actually execute it."
        }
    };
    let submitted = h.call(
        "session/prompt",
        "target",
        json!({"prompt":[{"type":"text","text":text}]}),
    );
    let cancellation = async {
        if case == "queued" {
            tokio::time::sleep(Duration::from_millis(500)).await;
            assert_eq!(h.frames("turn/start").len(), starts);
        } else {
            h.wait(|| (h.frames("turn/start").len() > starts).then_some(()))
                .await;
            if case == "approval" {
                h.wait(|| {
                    (h.frames("item/commandExecution/requestApproval").len() > approvals)
                        .then_some(())
                })
                .await;
                h.prompt("peer","Use only shell execution. Execute exactly: printf 'PEER_ALLOWED_04' > peer.txt . Then reply done.").await;
                h.wait(|| {
                    h.frames("item/commandExecution/requestApproval")
                        .iter()
                        .any(|r| r["params"]["threadId"] == peer.as_ref().unwrap()["sessionId"])
                        .then_some(())
                })
                .await;
                assert!(!h.root.join("target/forbidden.txt").exists());
            } else if case == "stream" {
                h.wait(|| {
                    (live
                        .rows()
                        .iter()
                        .filter(|e| e["type"] == "agent.message_chunk")
                        .count()
                        > previews)
                        .then_some(())
                })
                .await;
            } else if !matches!(case, "acceptance" | "late-turn") {
                h.wait(|| h.root.join("target/running.marker").exists().then_some(()))
                    .await;
                let pid: u32 = std::fs::read_to_string(h.root.join("target/running.pid"))
                    .unwrap()
                    .trim()
                    .parse()
                    .unwrap();
                let owned = h.owned_processes(h.runtime(&session)["pid"].as_u64().unwrap() as u32);
                assert!(
                    owned.contains_key(&pid),
                    "execution marker PID must belong to this native host"
                );
                std::fs::write(
                    h.root.join("execution-trigger.json"),
                    serde_json::to_vec_pretty(&json!({"pid":pid,"owned":owned})).unwrap(),
                )
                .unwrap();
            }
        }
        if case == "disconnect" {
            h.expect_transport_loss_cleanup();
        }
        let blocked = async {
            tokio::time::sleep(Duration::from_millis(25)).await;
            h.call(
                "session/prompt",
                "target",
                json!({"prompt":[{"type":"text","text":"OLD_MUST_NOT_REACH_NATIVE"}]}),
            )
            .await
        };
        let pending_ack = async {
            if case == "ack-late" {
                h.wait(|| {
                    h.frames("turn/completed")
                        .iter()
                        .any(|r| r["params"]["turn"]["status"] == "interrupted")
                        .then_some(())
                })
                .await;
                blocked_prompt(h, "ACK_PENDING_MUST_NOT_REACH_NATIVE").await;
            }
        };
        let (a, b, c, d, blocked, ()) = tokio::join!(
            h.call("session/cancel", "target", json!({})),
            h.call("session/cancel", "target", json!({})),
            h.call("session/cancel", "target", json!({})),
            h.call("session/cancel", "target", json!({})),
            blocked,
            pending_ack
        );
        let replies = [a, b, c, d];
        let blocked_kind = blocked["error"]["data"]["kind"].as_str();
        assert!(
            matches!(blocked_kind, Some("busy" | "cancelling"))
                || (case == "disconnect" && blocked_kind == Some("offline")),
            "{blocked}"
        );
        if case == "disconnect" {
            for reply in &replies {
                assert_eq!(reply["error"]["data"]["kind"], "cancel_unknown");
            }
            h.offline("target").await;
        } else if matches!(case, "unknown" | "late-turn") {
            assert_eq!(
                replies
                    .iter()
                    .map(|reply| reply["error"]["data"]["operationId"].as_str().unwrap())
                    .collect::<BTreeSet<_>>()
                    .len(),
                4
            );
            for reply in &replies {
                assert_eq!(reply["error"]["data"]["kind"], "cancel_unknown");
                assert_same_round(&reply["error"], &replies[0]["error"]);
            }
            blocked_prompt(h, "UNKNOWN_MUST_NOT_REACH_NATIVE").await;
            if case == "late-turn" {
                assert!(h.frames("turn/interrupt").is_empty());
            }
            tokio::time::sleep(Duration::from_secs(6)).await;
            blocked_prompt(h, "LATE_ACK_MUST_NOT_REACH_NATIVE").await;
            assert_same_round(
                &h.call("session/cancel", "target", json!({})).await["error"],
                &replies[0]["error"],
            );
        } else {
            for reply in &replies {
                assert_eq!(reply["result"], json!({}), "{reply}");
            }
        }
        replies
    };
    let (receipt, replies) = tokio::join!(submitted, cancellation);
    if case == "late-turn" {
        assert_eq!(receipt["error"]["data"]["kind"], "acceptance_unknown");
        assert_eq!(receipt["error"]["data"]["nativeAccepted"], false);
        assert_eq!(receipt["error"]["data"]["nativeRecorded"], false);
    }
    if case == "terminal-late" {
        blocked_prompt(h, "TERMINAL_PENDING_MUST_NOT_REACH_NATIVE").await;
    }
    if case == "approval" {
        blocked_prompt(h, "POLICY_PENDING_MUST_NOT_REACH_NATIVE").await;
        h.idle("peer").await;
        assert_eq!(
            std::fs::read_to_string(h.root.join("peer/peer.txt")).unwrap(),
            "PEER_ALLOWED_04"
        );
        let wire = h.wire();
        for request in h.frames("item/commandExecution/requestApproval") {
            let target = request["params"]["threadId"] == session["sessionId"];
            if target {
                assert!(
                    !wire
                        .as_array()
                        .unwrap()
                        .iter()
                        .any(|r| r["direction"] == "client"
                            && r["payload"]["id"] == request["id"]
                            && r["payload"]["result"]["decision"] == "accept")
                );
                assert!(
                    wire.as_array()
                        .unwrap()
                        .iter()
                        .any(|r| r["direction"] == "local"
                            && r["payload"]["method"] == "approval/staleDecisionSuppressed"
                            && r["payload"]["id"] == request["id"])
                );
            } else {
                assert!(
                    wire.as_array()
                        .unwrap()
                        .iter()
                        .any(|r| r["direction"] == "client"
                            && r["payload"]["id"] == request["id"]
                            && r["payload"]["result"]["decision"] == "accept")
                );
            }
        }
        h.assert_history("peer").await;
    }
    if !matches!(case, "queued" | "disconnect") {
        let interrupt = h.wait(|| h.frames("turn/interrupt").first().cloned()).await;
        assert_eq!(h.frames("turn/interrupt").len(), 1);
        h.wait(|| {
            h.frames("turn/completed")
                .iter()
                .any(|r| {
                    r["params"]["turn"]["id"] == interrupt["params"]["turnId"]
                        && r["params"]["turn"]["status"] == "interrupted"
                })
                .then_some(())
        })
        .await;
        h.wait(|| {
            h.wire()
                .as_array()
                .unwrap()
                .iter()
                .any(|r| {
                    r["direction"] == "server"
                        && r["payload"]["id"] == interrupt["id"]
                        && r["payload"].get("result").is_some()
                })
                .then_some(())
        })
        .await;
    } else if case == "queued" {
        assert!(h.frames("turn/interrupt").is_empty());
        assert_eq!(h.frames("turn/start").len(), starts);
        assert_eq!(receipt["error"]["data"]["kind"], "cancelled");
    }
    if case == "late-turn" {
        h.idle("target").await;
        blocked_prompt(h, "SETTLED_LATE_ACK_MUST_NOT_REACH_NATIVE").await;
        let repeated = h.call("session/cancel", "target", json!({})).await;
        assert_ne!(
            repeated["error"]["data"]["operationId"],
            replies[0]["error"]["data"]["operationId"]
        );
        assert_same_round(&repeated["error"], &replies[0]["error"]);
    }
    assert!(!h.root.join("target/forbidden.txt").exists());
    if !matches!(case, "unknown" | "late-turn" | "disconnect") {
        let deadline = Instant::now() + Duration::from_secs(15);
        loop {
            let next=h.call("session/prompt","target",json!({"prompt":[{"type":"text","text":"Without tools reply only MASS_NEW_WORK_04."}]})).await;
            if next.get("result").is_some() {
                break;
            }
            assert_eq!(next["error"]["data"]["kind"], "cancelling");
            assert!(Instant::now() < deadline);
            tokio::time::sleep(Duration::from_millis(25)).await;
        }
        h.idle("target").await;
    }
    let history = h.history("target").await;
    assert_eq!(history[..preserved.len()], preserved);
    for forbidden in [
        "OLD_MUST_NOT_REACH_NATIVE",
        "ACK_PENDING_MUST_NOT_REACH_NATIVE",
        "TERMINAL_PENDING_MUST_NOT_REACH_NATIVE",
        "POLICY_PENDING_MUST_NOT_REACH_NATIVE",
        "UNKNOWN_MUST_NOT_REACH_NATIVE",
        "LATE_ACK_MUST_NOT_REACH_NATIVE",
        "SETTLED_LATE_ACK_MUST_NOT_REACH_NATIVE",
    ] {
        assert!(!h.wire().to_string().contains(forbidden));
    }
    let complete: BTreeSet<_> = history
        .iter()
        .filter_map(|e| e["itemId"].as_str().map(String::from))
        .collect();
    let chunks: BTreeSet<_> = live
        .rows()
        .iter()
        .filter(|e| e["type"].as_str().is_some_and(|s| s.ends_with("_chunk")))
        .filter_map(|e| e["itemId"].as_str().map(String::from))
        .collect();
    let partial: Vec<_> = chunks.difference(&complete).cloned().collect();
    if case == "stream" {
        assert!(!partial.is_empty(), "must interrupt a real delta-only item");
    }
    h.assert_history("target").await;
    h.wait(|| {
        (live
            .rows()
            .iter()
            .filter(|e| e.get("seq").is_some())
            .cloned()
            .collect::<Vec<_>>()
            == history)
            .then_some(())
    })
    .await;
    std::fs::write(h.root.join("result.json"),serde_json::to_vec_pretty(&json!({"passed":true,"migratedFrom":"codex_cancel_e2e.py","case":case,"cancellations":replies,"receipt":receipt,"chunksOnly":partial,"historyCount":history.len(),"preservedCount":preserved.len()})).unwrap()).unwrap();
}
async fn blocked_prompt(h: &Native, text: &str) {
    let response = h
        .call(
            "session/prompt",
            "target",
            json!({"prompt":[{"type":"text","text":text}]}),
        )
        .await;
    assert_eq!(
        response["error"]["data"]["kind"], "cancelling",
        "{response}"
    );
}

fn assert_same_round(first: &serde_json::Value, second: &serde_json::Value) {
    assert!(first["data"]["operationId"].is_string());
    assert!(second["data"]["operationId"].is_string());
    let mut first = first.clone();
    let mut second = second.clone();
    first["data"].as_object_mut().unwrap().remove("operationId");
    second["data"]
        .as_object_mut()
        .unwrap()
        .remove("operationId");
    assert_eq!(first, second);
}
