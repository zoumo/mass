use crate::support::{Live, Native};
use serde_json::{Value, json};
use std::time::{Duration, Instant};

async fn equality(h: &Native, name: &str, live: &Live) -> Vec<Value> {
    let rows = h.history(name).await;
    h.assert_history(name).await;
    h.wait(|| {
        (live
            .rows()
            .iter()
            .filter(|e| e.get("seq").is_some())
            .cloned()
            .collect::<Vec<_>>()
            == rows)
            .then_some(())
    })
    .await;
    rows
}
fn fixture(h: &Native, prefix: &str, suffix: &str) -> String {
    let expected = format!("{prefix}{}{suffix}", "Z".repeat(2097152));
    std::fs::write(h.root.join("target/source-output.txt"), &expected).unwrap();
    expected
}

pub async fn run(h: &Native, case: &str) {
    // The legacy fault runner explicitly reaped observed external hook/MCP descendants
    // after product exit. Native host reaping remains a strict product assertion.
    h.allow_captured_descendant_cleanup();
    let target = h.new_session("target").await;
    let peer = h.new_session("peer").await;
    assert_eq!(h.runtime(&target)["pid"], h.runtime(&peer)["pid"]);
    let mut live = h.live("target").await;
    let peer_live = h.live("peer").await;
    let journal = h.session_dir(&target).join("events.jsonl");
    match case {
        "slow" => {
            let at = Instant::now();
            h.prompt("target","Without tools, write 100 numbered sentences about rivers. End with SLOW06_COMPLETE.").await;
            assert!(at.elapsed() < Duration::from_secs(6));
            assert!(!journal.exists());
            let at = Instant::now();
            h.prompt("peer", "Without tools, reply only PEER06_CONTINUES.")
                .await;
            assert!(at.elapsed() < Duration::from_secs(6));
            h.idle("peer").await;
            h.idle_with("target", 240).await;
            assert!(
                equality(h, "target", &live)
                    .await
                    .iter()
                    .any(|e| e["type"] == "agent.message"
                        && e["payload"].to_string().contains("SLOW06_COMPLETE"))
            );
            equality(h, "peer", &peer_live).await;
        }
        "long-output" => {
            let expected = fixture(h, "NATIVE_LONG06_", "END_LONG06");
            h.prompt("target","Use shell to execute exactly one command: cp source-output.txt long06.txt; cat long06.txt; printf '\\n' . Then without tools write 100 numbered short sentences about forests and end with LONG06_COMPLETE.").await;
            h.idle_with("target", 240).await;
            assert_eq!(
                std::fs::read_to_string(h.root.join("target/long06.txt")).unwrap(),
                expected
            );
            let rows = equality(h, "target", &live).await;
            let result = rows
                .iter()
                .find(|e| e["type"] == "agent.tool_result")
                .unwrap();
            assert!(serde_json::to_vec(result).unwrap().len() > 500000);
        }
        "append" => {
            h.prompt("target", "Without tools reply BASE06_PRESERVED.")
                .await;
            h.idle("target").await;
            let original = equality(h, "target", &live).await;
            let bytes = std::fs::read(&journal).unwrap();
            std::fs::remove_file(&journal).unwrap();
            std::fs::create_dir(&journal).unwrap();
            h.call(
                "session/prompt",
                "target",
                json!({"prompt":[{"type":"text","text":"Without tools reply UNCOMMITTED06."}]}),
            )
            .await;
            h.offline("target").await;
            assert!(h.result("session/status", "target", json!({})).await["state"].is_null());
            h.wait(|| {
                live.rows()
                    .iter()
                    .any(|e| e["type"] == "session.error" && e.to_string().contains("append"))
                    .then_some(())
            })
            .await;
            std::fs::remove_dir(&journal).unwrap();
            std::fs::write(&journal, &bytes).unwrap();
            assert!(
                h.call(
                    "session/prompt",
                    "target",
                    json!({"prompt":[{"type":"text","text":"SEALED_WRITER_MUST_NOT_SEND"}]})
                )
                .await
                .get("error")
                .is_some()
            );
            assert_eq!(equality(h, "target", &live).await, original);
            h.prompt("peer", "Without tools reply PEER06_STORAGE_SURVIVED.")
                .await;
            h.idle("peer").await;
            equality(h, "peer", &peer_live).await;
            h.released("target").await;
            h.result("session/resume", "target", json!({})).await;
            h.reconnect(&mut live, "target").await;
            h.prompt("target", "Without tools reply only RECOVERED_WRITER06.")
                .await;
            h.idle("target").await;
            let rows = equality(h, "target", &live).await;
            assert_eq!(rows[..original.len()], original);
            assert!(!json!(rows).to_string().contains("UNCOMMITTED06"));
        }
        "backpressure" => {
            let expected = fixture(h, "PRESSURE06_", "END_PRESSURE06");
            h.prompt("target","First send a short commentary message START_PRESSURE06. Then use shell to execute exactly: cp source-output.txt pressure06.txt; cat pressure06.txt; printf '\\n' . Afterwards without tools write 100 numbered sentences about mountains and end with PRESSURE06_COMPLETE.").await;
            h.wait(|| {
                h.frames("core/backpressure")
                    .iter()
                    .any(|r| r["durable"] == true)
                    .then_some(())
            })
            .await;
            let at = Instant::now();
            let receipt = h.call("session/prompt","peer",json!({"prompt":[{"type":"text","text":"Without tools reply PEER06_PRESSURE_CONTINUES."}]})).await;
            assert!(
                receipt.get("result").is_some()
                    || receipt["error"]["data"]["kind"] == "acceptance_unknown",
                "{receipt}"
            );
            std::fs::write(
                h.root.join("peer-pressure.json"),
                json!({"elapsedMs":at.elapsed().as_millis(),"receipt":receipt}).to_string(),
            )
            .unwrap();
            h.idle_with("target", 240).await;
            h.idle("peer").await;
            for name in ["target", "peer"] {
                assert_eq!(
                    h.result("session/status", name, json!({})).await["connected"],
                    true
                );
            }
            assert_eq!(
                std::fs::read_to_string(h.root.join("target/pressure06.txt")).unwrap(),
                expected
            );
            let rows = equality(h, "target", &live).await;
            equality(h, "peer", &peer_live).await;
            assert!(rows.iter().any(|e| e["type"] == "agent.message"
                && e["payload"].to_string().contains("PRESSURE06_COMPLETE")));
            h.shutdown().await;
            let stats = h.frames("sdk/eventBufferHighWater");
            assert!(!stats.is_empty());
            assert!(stats.iter().all(|r| r["count"].as_u64().unwrap()
                <= r["countLimit"].as_u64().unwrap()
                && r["bytes"].as_u64().unwrap() <= r["byteLimit"].as_u64().unwrap()));
            assert!(
                stats.iter().any(|r| r["count"] == 32),
                "real sustained traffic must fill the official queue"
            );
            assert!(
                h.frames("core/laneSealed")
                    .iter()
                    .any(|r| r["highCount"] == 3 && r["droppedPreviews"].as_u64().unwrap() > 0)
            );
        }
        "sdk-oversize" => {
            let expected = fixture(h, "OVERSIZE06_", "END_OVERSIZE06");
            h.prompt("target","Use shell to execute exactly: cp source-output.txt oversize06.txt; cat oversize06.txt; printf '\\n' . Then reply OVERSIZE06_COMPLETE.").await;
            tokio::join!(h.offline("target"), h.offline("peer"));
            h.wait(|| {
                live.rows()
                    .iter()
                    .any(|e| {
                        e["type"] == "session.error"
                            && e["payload"]["reason"] == "attachment_unavailable"
                    })
                    .then_some(())
            })
            .await;
            assert_eq!(
                std::fs::read_to_string(h.root.join("target/oversize06.txt")).unwrap(),
                expected
            );
            assert!(
                h.frames("core/laneSealed").iter().any(|r| r["reason"]
                    .as_str()
                    .is_some_and(|reason| reason.contains("consumer budget"))),
                "private diagnostic must preserve the actual SDK budget failure"
            );
            let rows = equality(h, "target", &live).await;
            assert!(!rows.iter().any(|e| e["type"] == "agent.tool_result"));
            for name in ["target", "peer"] {
                assert!(h.result("session/status", name, json!({})).await["state"].is_null());
            }
        }
        "stop-stream" | "stop-drain" => {
            if case == "stop-stream" {
                h.prompt(
                    "target",
                    "Without tools write 1000 long numbered sentences about stars.",
                )
                .await;
                h.wait_wire("item/agentMessage/delta").await;
            } else {
                h.prompt("target", "Without tools reply STOP06_COMPLETE.")
                    .await;
                h.wait(|| {
                    h.frames("item/completed")
                        .iter()
                        .any(|r| r["params"]["item"]["type"] == "agentMessage")
                        .then_some(())
                })
                .await;
            }
            h.shutdown().await;
            let rows = equality(h, "target", &live).await;
            if case == "stop-drain" {
                assert!(json!(rows).to_string().contains("STOP06_COMPLETE"));
            }
        }
        "host-loss" => host_loss(h, &target, &mut live).await,
        _ => panic!("unknown fault case"),
    }
    std::fs::write(
        h.root.join("result.json"),
        json!({"passed":true,"migratedFrom":"codex_faults_e2e.py","case":case}).to_string(),
    )
    .unwrap();
}
async fn host_loss(h: &Native, target: &Value, live: &mut Live) {
    h.new_session("pending").await;
    let independent = h.new_session_on("independent", "other").await;
    let independent_live = h.live("independent").await;
    assert_ne!(h.runtime(&independent)["pid"], h.runtime(target)["pid"]);
    h.prompt("target", "Without tools reply PRESERVED06.").await;
    h.idle("target").await;
    let original = equality(h, "target", live).await;
    let old = h.captured_commands();
    h.prompt(
        "target",
        "Use shell to run exactly: printf FORBIDDEN06 > forbidden06.txt. Then reply DONE.",
    )
    .await;
    let approval = h
        .wait(|| {
            h.frames("item/commandExecution/requestApproval")
                .into_iter()
                .find(|r| r["params"]["threadId"] == target["sessionId"])
        })
        .await;
    h.prompt(
        "peer",
        "Without tools write 200 long sentences about the sea.",
    )
    .await;
    let peer = h.result("session/status", "peer", json!({})).await;
    h.wait(|| {
        h.frames("item/agentMessage/delta")
            .iter()
            .any(|r| r["params"]["threadId"] == peer["sessionId"])
            .then_some(())
    })
    .await;
    let pending = h.call(
        "session/prompt",
        "pending",
        json!({"prompt":[{"type":"text","text":"PENDING06_MUST_NOT_REPLAY"}]}),
    );
    let kill = async {
        tokio::time::sleep(Duration::from_millis(100)).await;
        assert!(
            !h.wire()
                .as_array()
                .unwrap()
                .iter()
                .any(|r| r["direction"] == "client"
                    && r["payload"]["id"] == approval["id"]
                    && (r["payload"].get("result").is_some()
                        || r["payload"].get("error").is_some()))
        );
        assert!(!h.root.join("target/forbidden06.txt").exists());
        h.kill_host("target").await;
    };
    let (unknown, ()) = tokio::join!(pending, kill);
    assert_eq!(unknown["error"]["data"]["kind"], "acceptance_unknown");
    tokio::join!(h.offline("target"), h.offline("peer"));
    for name in ["target", "peer"] {
        assert!(h.result("session/status", name, json!({})).await["state"].is_null());
    }
    h.prompt(
        "independent",
        "Without tools reply INDEPENDENT06_CONTINUES.",
    )
    .await;
    h.idle("independent").await;
    equality(h, "independent", &independent_live).await;
    h.released("target").await;
    let (resumed, new) = tokio::join!(
        h.result("session/resume", "target", json!({})),
        h.new_session("new")
    );
    h.reconnect(live, "target").await;
    assert_eq!(h.runtime(&resumed)["pid"], h.runtime(&new)["pid"]);
    assert_ne!(h.runtime(&resumed)["pid"], h.runtime(target)["pid"]);
    for name in ["peer", "pending"] {
        assert_eq!(
            h.result("session/status", name, json!({})).await["connected"],
            false
        );
    }
    assert!(
        old.prompt(vec![
            json!({"type":"text","text":"OLD_ATTACHMENT_MUST_NOT_REACH_NATIVE"})
        ])
        .await
        .is_err()
    );
    assert!(old.cancel().await.is_err());
    assert!(old.set_config_option("model", "stale").await.is_err());
    assert!(!h.root.join("target/forbidden06.txt").exists());
    h.prompt("target", "Without tools reply RECOVERED06.").await;
    h.idle("target").await;
    let rows = equality(h, "target", live).await;
    assert_eq!(rows[..original.len()], original);
    assert!(
        !h.wire()
            .to_string()
            .contains("OLD_ATTACHMENT_MUST_NOT_REACH_NATIVE")
    );
    assert!(!h.wire().to_string().contains("PENDING06_MUST_NOT_REPLAY"));
    assert_eq!(
        std::fs::read_dir(h.root.join("native"))
            .unwrap()
            .filter_map(Result::ok)
            .filter(|e| e.path().join("spawn.json").exists())
            .count(),
        3
    );
}
