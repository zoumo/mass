use crate::support::Native;
use serde_json::{Value, json};

pub async fn run(h: &Native, truncated: bool, delayed: bool) {
    let session = h.new_session("target").await;
    let expected = "MASS_TOOL_INPUT_cedar_0317\n";
    let written = "MASS_TOOL_OUTPUT_maple_5391\n";
    std::fs::write(h.root.join("target/input.txt"), expected).unwrap();
    if truncated {
        std::fs::write(h.root.join("target/large-input.txt"), "Z".repeat(2097152)).unwrap();
    }
    let peer = if delayed {
        Some(h.new_session("peer").await)
    } else {
        None
    };
    let live = h.live("target").await;
    let mut text="Use only the shell command execution tool, no apply_patch or other tools. Execute these THREE separate shell calls in order, waiting for each: (1) cat input.txt (2) printf 'MASS_TOOL_OUTPUT_maple_5391\\n' > output.txt (3) cat output.txt . Use the session cwd. Do not combine the calls. Then answer with the two file contents. You must actually execute the commands.".to_owned();
    if truncated {
        text.push_str(" After those three calls, execute one separate fourth shell call: cat large-input.txt . Do not redirect or pipe its output. Then give a short final reply; do not repeat the large output.");
    }
    let receipt = h.prompt("target", &text).await;
    let peer_receipt = if delayed {
        h.wait_wire("item/commandExecution/requestApproval").await;
        Some(
            h.prompt(
                "peer",
                "Do not use tools. Reply with only MASS_PEER_APPROVAL_READER_0317.",
            )
            .await,
        )
    } else {
        None
    };
    h.idle_with("target", 240).await;
    if delayed {
        h.idle("peer").await;
        let history = h.history("peer").await;
        assert!(history.iter().any(|e| {
            e["type"] == "agent.message"
                && e["payload"]["content"]
                    .to_string()
                    .contains("MASS_PEER_APPROVAL_READER_0317")
        }));
        assert!(
            !history
                .iter()
                .any(|e| e["type"].as_str().unwrap().starts_with("agent.tool_"))
        );
        h.assert_history("peer").await;
    }
    let history = h.history("target").await;
    assert!(
        !json!(history)
            .to_string()
            .contains("MASS_PEER_APPROVAL_READER_0317")
    );
    let calls: Vec<_> = history
        .iter()
        .filter(|e| e["type"] == "agent.tool_call")
        .collect();
    let results: Vec<_> = history
        .iter()
        .filter(|e| e["type"] == "agent.tool_result")
        .collect();
    assert!(calls.len() >= 3);
    assert_eq!(calls.len(), results.len());
    assert_eq!(
        std::fs::read_to_string(h.root.join("target/output.txt")).unwrap(),
        written
    );
    assert_eq!(
        std::fs::read_to_string(h.root.join("target/input.txt")).unwrap(),
        expected
    );
    let wire = h.wire();
    let rows = wire.as_array().unwrap();
    let approvals = h.frames("item/commandExecution/requestApproval");
    assert!(!approvals.is_empty());
    if let Some(peer_receipt) = &peer_receipt {
        let request = rows
            .iter()
            .position(|r| r["payload"]["method"] == "item/commandExecution/requestApproval")
            .unwrap();
        let answer = rows
            .iter()
            .position(|r| {
                r["direction"] == "client"
                    && r["payload"]["id"] == approvals[0]["id"]
                    && r["payload"].get("method").is_none()
            })
            .unwrap();
        let echo = rows
            .iter()
            .position(|r| {
                r["payload"]["method"] == "item/completed"
                    && r["payload"]["params"]["item"]["clientId"] == peer_receipt["submissionId"]
            })
            .unwrap();
        assert!(
            request < echo && echo < answer,
            "native reader/writer must progress during actual approval delay"
        );
    }
    for approval in &approvals {
        let answers: Vec<_> = rows
            .iter()
            .filter(|r| {
                r["direction"] == "client"
                    && r["payload"]["id"] == approval["id"]
                    && r["payload"].get("method").is_none()
            })
            .map(|r| r["payload"].clone())
            .collect();
        assert_eq!(
            answers,
            vec![json!({"id":approval["id"],"result":{"decision":"accept"}})]
        );
        assert!(
            h.frames("serverRequest/resolved")
                .iter()
                .any(|r| r["params"]["requestId"] == approval["id"]
                    && r["params"]["threadId"] == session["sessionId"])
        );
    }
    for call in &calls {
        let private_call = h.private_payload(call);
        let p = &private_call;
        let native = &p["nativeItem"];
        assert_eq!(p["inputRepresentation"], "normalized_execution");
        assert_eq!(p["phase"], "parameters_ready");
        assert_eq!(
            p["rawInput"],
            json!({"command":native["command"],"cwd":native["cwd"],"commandActions":native["commandActions"]})
        );
        let result = results
            .iter()
            .find(|e| e["itemId"] == call["itemId"])
            .unwrap();
        assert!(result["seq"].as_u64().unwrap() > call["seq"].as_u64().unwrap());
        let private_result = h.private_payload(result);
        let q = &private_result;
        let full = &q["nativeResult"];
        assert_eq!(q["callContext"], *p);
        assert_eq!(result["payload"]["callContext"], call["payload"]);
        assert_eq!(
            result["payload"]["output"]["availability"],
            q["output"]["availability"]
        );
        assert_eq!(
            result["payload"]["output"]["completeness"],
            if q["output"]["completeness"] == "native_retained" {
                json!("retained")
            } else {
                q["output"]["completeness"].clone()
            }
        );
        if q["output"]["completeness"] == "truncated" {
            assert_eq!(
                result["payload"]["output"]["truncation"]["omittedBytes"],
                q["output"]["truncationEvidence"]["omittedBytes"]
            );
            assert!(
                result["payload"]["output"]
                    .get("truncationEvidence")
                    .is_none()
            );
        }
        assert_eq!(q["nativeStatus"], full["status"]);
        assert_eq!(q["exitCode"], 0);
        assert_eq!(q["exitCode"], full["exitCode"]);
        assert_eq!(q["durationMs"], full["durationMs"]);
        if full["aggregatedOutput"].is_null() {
            assert_eq!(
                q["output"],
                json!({"availability":"unavailable","completeness":"unknown"})
            );
            assert!(q.get("rawOutput").is_none());
            assert!(q.get("content").is_none());
        } else if truncated
            && full["aggregatedOutput"]
                .as_str()
                .unwrap()
                .contains("bytes omitted")
        {
            assert_eq!(
                full["aggregatedOutput"],
                format!(
                    "{}\n... 1048576 bytes omitted ...\n{}",
                    "Z".repeat(524288),
                    "Z".repeat(524288)
                )
            );
            assert_eq!(
                q["output"],
                json!({"availability":"provided","completeness":"truncated","truncationEvidence":{"kind":"codex_0_160_1_head_tail_marker","omittedBytes":1048576,"retainedBytes":1048576}})
            );
            assert_eq!(q["rawOutput"], full["aggregatedOutput"]);
        } else {
            assert_eq!(
                q["output"],
                json!({"availability":"provided","completeness":"native_retained"})
            );
            assert_eq!(q["rawOutput"], full["aggregatedOutput"]);
            assert_eq!(
                q["content"],
                json!([{"type":"text","text":full["aggregatedOutput"]}])
            );
        }
        for event in [*call, *result] {
            let source = &h.private_payload(event)["source"];
            assert_eq!(source["threadId"], session["sessionId"]);
            assert_eq!(source["nativeSessionId"], session["sessionId"]);
            for field in ["connectionId", "hostGeneration", "turnId"] {
                assert!(!source[field].as_str().unwrap().is_empty());
            }
        }
    }
    assert!(
        results
            .iter()
            .any(|e| h.private_payload(e)["nativeResult"]["aggregatedOutput"].is_null())
    );
    assert!(
        results
            .iter()
            .any(|e| e["payload"]["rawOutput"] == expected)
    );
    assert!(results.iter().any(|e| e["payload"]["rawOutput"] == written));
    if truncated {
        assert!(
            results
                .iter()
                .any(|e| e["payload"]["output"]["completeness"] == "truncated")
        );
    }
    assert!(
        session["capabilities"]["tools"]
            .as_array()
            .unwrap()
            .contains(&json!("shell"))
    );
    h.assert_history("target").await;
    h.wait(|| {
        (live
            .rows()
            .iter()
            .filter(|e| e.get("seq").is_some())
            .cloned()
            .collect::<Vec<Value>>()
            == history)
            .then_some(())
    })
    .await;
    std::fs::write(h.root.join("result.json"),json!({"passed":true,"migratedFrom":"codex_tools_e2e.py","truncation":truncated,"delayed":delayed,"receipt":receipt,"peer":peer,"peerReceipt":peer_receipt,"calls":calls.len(),"results":results.len(),"approvals":approvals.len()}).to_string()).unwrap();
}
