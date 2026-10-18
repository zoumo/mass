use crate::support::Native;
use serde_json::{Value, json};

pub async fn run(h: &Native, full_only: bool) {
    assert_eq!(h.result("status", "", json!({})).await["connected"], 0);
    let session = h.new_session("target").await;
    assert_eq!(h.runtime(&session)["version"], "0.160.1");
    assert_eq!(
        h.runtime(&session)["activationModel"],
        "runtime_scoped_server"
    );
    assert_eq!(session["capabilities"]["inputTypes"], json!(["text"]));
    for capability in ["cancel", "archive"] {
        assert_eq!(session["capabilities"][capability], true);
    }
    for capability in [
        "resume",
        "setConfigOption",
        "interactiveRequests",
        "backgroundLifecycle",
        "concurrentInput",
        "steer",
    ] {
        assert_eq!(session["capabilities"][capability], false);
    }
    assert!(h.history("target").await.is_empty());
    assert_eq!(
        h.result("session/cancel", "target", json!({})).await,
        json!({})
    );
    assert_eq!(
        h.call("session/set_config_option", "target", json!({}))
            .await["error"]["data"]["kind"],
        "unsupported_capability"
    );
    assert_eq!(
        h.call("session/respond", "target", json!({"answer":"yes"}))
            .await["error"]["code"],
        -32601
    );
    assert_eq!(
        h.call(
            "session/prompt",
            "target",
            json!({"prompt":[{"type":"image","data":"invalid"}]})
        )
        .await["error"]["code"],
        -32602
    );
    let live = h.live("target").await;
    let mut receipts = Vec::new();
    for prompt in [
        "Remember the noncredential token MASS_NATIVE_CEDAR_8472. Without using any tools, send a short commentary message, then a separate final answer repeating this token. Do not inspect any files.",
        "Without tools, repeat the token I asked you to remember in the previous turn. Send only that token.",
    ] {
        let pending = h.prompt("target", prompt);
        let probe = async {
            h.wait(|| (h.frames("turn/start").len() > receipts.len()).then_some(()))
                .await;
            assert_eq!(
                h.call(
                    "session/prompt",
                    "target",
                    json!({"prompt":[{"type":"text","text":"MUST_NOT_BE_SENT"}]})
                )
                .await["error"]["data"]["kind"],
                "busy"
            );
        };
        let (receipt, ()) = tokio::join!(pending, probe);
        receipts.push(receipt);
        h.idle("target").await;
    }
    let history = h.history("target").await;
    let users: Vec<_> = history
        .iter()
        .filter(|e| e["type"] == "user.message")
        .collect();
    assert_eq!(users.len(), 2);
    assert_eq!(
        users
            .iter()
            .map(|e| e["itemId"].clone())
            .collect::<Vec<_>>(),
        receipts
            .iter()
            .map(|r| r["messageId"].clone())
            .collect::<Vec<_>>()
    );
    assert_eq!(
        users
            .iter()
            .map(|e| h.private_payload(e)["source"]["clientId"].clone())
            .collect::<Vec<_>>(),
        receipts
            .iter()
            .map(|r| r["submissionId"].clone())
            .collect::<Vec<_>>()
    );
    let replies: Vec<_> = history
        .iter()
        .filter(|e| e["type"] == "agent.message")
        .collect();
    assert!(
        replies.len() >= 3,
        "first turn requires independent commentary and final full items"
    );
    assert!(
        replies.last().unwrap()["payload"]["content"]
            .to_string()
            .contains("MASS_NATIVE_CEDAR_8472")
    );
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
    let mut paged = Vec::new();
    let mut after = 0;
    loop {
        let page = h
            .result(
                "session/get_event",
                "target",
                json!({"afterSeq":after,"limit":1}),
            )
            .await;
        let rows = page["events"].as_array().unwrap();
        if rows.is_empty() {
            break;
        }
        paged.extend(rows.clone());
        after = page["nextSeq"].as_u64().unwrap();
    }
    assert_eq!(paged, history);
    let starts = h.frames("turn/start");
    assert_eq!(starts.len(), 2);
    assert!(
        starts
            .iter()
            .all(|r| r["params"]["threadId"] == session["sessionId"])
    );
    for (user, start) in users.iter().zip(starts.iter()) {
        assert_eq!(
            h.private_payload(user)["source"]["clientId"],
            start["params"]["clientUserMessageId"]
        );
    }
    for event in &history {
        let source = &h.private_payload(event)["source"];
        for field in ["connectionId", "hostGeneration"] {
            assert!(!source[field].as_str().unwrap().is_empty());
        }
        let native = &h.private_payload(event)["nativeItem"];
        let matching: Vec<_> = h
            .frames("item/completed")
            .into_iter()
            .filter(|r| r["params"]["item"]["id"] == source["itemId"])
            .collect();
        assert_eq!(matching.len(), 1);
        assert_eq!(matching[0]["params"]["item"], *native);
        if event["type"] == "user.message" {
            assert_eq!(h.private_payload(event)["content"], native["content"]);
            assert_eq!(
                event["payload"]["content"],
                json!(
                    native["content"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .map(|b| json!({"type":"text","text":b["text"]}))
                        .collect::<Vec<_>>()
                )
            );
        }
        if event["type"] == "agent.message" {
            assert_eq!(
                event["payload"]["content"],
                json!([{"type":"text","text":native["text"]}])
            );
        }
    }
    assert!(!h.frames("item/agentMessage/delta").is_empty());
    if full_only {
        assert!(
            !live
                .rows()
                .iter()
                .any(|e| e["type"].as_str().unwrap().ends_with("_chunk"))
        );
    } else {
        for preview in live
            .rows()
            .iter()
            .filter(|e| e["type"] == "agent.message_chunk")
        {
            assert!(replies.iter().any(|r| r["itemId"] == preview["itemId"]));
        }
    }
    let turn = &h.private_payload(users[0])["source"]["turnId"];
    let wire = h.wire();
    let frames: Vec<_> = wire
        .as_array()
        .unwrap()
        .iter()
        .filter(|r| r["direction"] == "server")
        .collect();
    let completed: Vec<_> = frames
        .iter()
        .enumerate()
        .filter(|(_, r)| {
            r["payload"]["method"] == "item/completed"
                && r["payload"]["params"]["turnId"] == *turn
                && r["payload"]["params"]["item"]["type"] == "agentMessage"
        })
        .map(|(i, _)| i)
        .collect();
    let terminal = frames
        .iter()
        .position(|r| {
            r["payload"]["method"] == "turn/completed"
                && r["payload"]["params"]["turn"]["id"] == *turn
        })
        .unwrap();
    assert!(completed.len() >= 2 && completed.iter().all(|i| *i < terminal));
    let terminal_ns: u64 = frames[terminal]["time"].as_str().unwrap().parse().unwrap();
    let early: Vec<Value> = live
        .observations()
        .into_iter()
        .filter(|r| {
            r["event"]["type"] == "agent.message"
                && h.private_payload(&r["event"])["source"]["turnId"] == *turn
        })
        .collect();
    assert!(
        early.len() >= 2
            && early
                .iter()
                .all(|r| r["receivedAtNs"].as_u64().unwrap() < terminal_ns),
        "full SSE receipt must precede the native foreground terminal: {early:?}"
    );
    std::fs::write(
        h.root.join("sse-observations.json"),
        serde_json::to_vec_pretty(&live.observations()).unwrap(),
    )
    .unwrap();
    std::fs::write(h.root.join("result.json"),json!({"passed":true,"migratedFrom":"codex_text_e2e.py","fullOnly":full_only,"session":session,"receipts":receipts,"durableCount":history.len()}).to_string()).unwrap();
}

pub async fn uncertainty(h: &Native, unknown: bool) {
    h.new_session("target").await;
    let params = json!({"sessionName":"target","prompt":[{"type":"text","text":"Without tools, reply with exactly MASS_LATE_FACT_CEDAR_915."}]});
    if unknown {
        let result = h.call("session/prompt", "target", params).await;
        assert_eq!(result["error"]["data"]["kind"], "acceptance_unknown");
        for (field, expected) in [
            ("sent", true),
            ("nativeRecorded", true),
            ("nativeAccepted", false),
        ] {
            assert_eq!(result["error"]["data"][field], expected);
        }
        assert_eq!(
            h.call(
                "session/prompt",
                "target",
                json!({"prompt":[{"type":"text","text":"MUST_NOT_RETRY"}]})
            )
            .await["error"]["data"]["kind"],
            "busy"
        );
    } else {
        use tokio::io::AsyncWriteExt;
        let body =
            json!({"jsonrpc":"2.0","id":"gone","method":"agentd/session/prompt","params":params})
                .to_string();
        let mut caller = tokio::net::TcpStream::connect(h.address).await.unwrap();
        caller.write_all(format!("POST /api/v1/agentd/jsonrpc HTTP/1.1\r\nHost: {}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{body}",h.address,body.len()).as_bytes()).await.unwrap();
        h.wait_wire("turn/start").await;
        drop(caller);
    }
    h.idle("target").await;
    let history = h.history("target").await;
    let users: Vec<_> = history
        .iter()
        .filter(|e| e["type"] == "user.message")
        .collect();
    assert_eq!(users.len(), 1);
    assert!(history.iter().any(|e| {
        e["type"] == "agent.message"
            && e["payload"]["content"]
                .to_string()
                .contains("MASS_LATE_FACT_CEDAR_915")
    }));
    let starts = h.frames("turn/start");
    assert_eq!(starts.len(), 1);
    assert_eq!(
        h.private_payload(users[0])["source"]["clientId"],
        starts[0]["params"]["clientUserMessageId"]
    );
    assert!(!h.wire().to_string().contains("MUST_NOT_RETRY"));
    h.assert_history("target").await;
    std::fs::write(h.root.join("result.json"),json!({"passed":true,"migratedFrom":"codex_text_uncertainty.py","scenario":if unknown {"unknown"} else {"caller_gone"},"historyCount":history.len()}).to_string()).unwrap();
}
