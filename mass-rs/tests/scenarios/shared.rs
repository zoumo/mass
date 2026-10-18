use crate::support::Native;
use serde_json::{Value, json};
use std::{
    collections::{BTreeMap, BTreeSet},
    time::Duration,
};

pub async fn run(h: &Native) {
    assert_eq!(h.result("status", "", json!({})).await["connected"], 0);
    let (cedar, birch) = tokio::join!(h.new_session("cedar"), h.new_session("birch"));
    assert_eq!(h.runtime(&cedar)["pid"], h.runtime(&birch)["pid"]);
    assert_ne!(cedar["sessionId"], birch["sessionId"]);
    assert_ne!(cedar["sessionId"], birch["sessionId"]);
    assert_eq!(h.result("status", "", json!({})).await["connected"], 2);
    let other = h.new_session_on("other", "other").await;
    assert_ne!(h.runtime(&other)["pid"], h.runtime(&cedar)["pid"]);
    assert_eq!(h.result("status", "", json!({})).await["connected"], 3);
    let sessions = BTreeMap::from([("cedar", cedar), ("birch", birch), ("other", other)]);
    let live_cedar = h.live("cedar").await;
    let live_birch = h.live("birch").await;
    let live_other = h.live("other").await;
    let tokens = BTreeMap::from([
        ("cedar", "MASS_CEDAR_49271"),
        ("birch", "MASS_BIRCH_63928"),
        ("other", "MASS_OTHER_82643"),
    ]);
    let cedar_text = format!(
        "Remember the noncredential token {}. Do not use any tools or inspect files. Output exactly 80 numbered lines, each containing this token and a short different sentence. Do not mention any other token.",
        tokens["cedar"]
    );
    let birch_text = format!(
        "Remember the noncredential token {}. Do not use any tools or inspect files. Output exactly 20 numbered lines, each containing this token and a short different sentence. Do not mention any other token.",
        tokens["birch"]
    );
    let prompts = async {
        tokio::join!(
            h.prompt("cedar", &cedar_text),
            h.prompt("birch", &birch_text)
        )
    };
    let states = async {
        let mut both = false;
        let mut distinct = false;
        tokio::time::timeout(Duration::from_secs(180), async {
            loop {
                let a = h.result("session/status", "cedar", json!({})).await["state"].clone();
                let b = h.result("session/status", "birch", json!({})).await["state"].clone();
                both |= a == "running" && b == "running";
                distinct |= (a == "running" && b == "idle") || (a == "idle" && b == "running");
                if a == "idle" && b == "idle" {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(50)).await;
            }
        })
        .await
        .expect("both native threads must finish");
        assert!(both, "two real threads must overlap in running state");
        assert!(distinct, "one terminal must not settle its peer");
    };
    let ((a, b), ()) = tokio::join!(prompts, states);
    let (a2, b2) = tokio::join!(
        h.prompt(
            "cedar",
            "Without tools, reply with only the token I asked you to remember in the previous turn."
        ),
        h.prompt(
            "birch",
            "Without tools, reply with only the token I asked you to remember in the previous turn."
        )
    );
    let c = h
        .prompt(
            "other",
            &format!("Without tools, reply with only {}.", tokens["other"]),
        )
        .await;
    tokio::join!(h.idle("cedar"), h.idle("birch"), h.idle("other"));
    let receipts = BTreeMap::from([
        ("cedar", vec![a, a2]),
        ("birch", vec![b, b2]),
        ("other", vec![c]),
    ]);
    let mut histories = BTreeMap::new();
    for (name, live) in [
        ("cedar", &live_cedar),
        ("birch", &live_birch),
        ("other", &live_other),
    ] {
        let history = h.history(name).await;
        let session = &sessions[name];
        assert!(
            live.rows()
                .iter()
                .all(|e| e["sessionName"] == name && e["sessionId"] == session["sessionId"])
        );
        assert!(
            history
                .iter()
                .all(
                    |e| h.private_payload(e)["source"]["threadId"] == session["sessionId"]
                        && h.private_payload(e)["source"]["hostGeneration"]
                            == h.runtime(session)["hostGeneration"]
                )
        );
        assert_eq!(
            history
                .iter()
                .filter(|e| e["type"] == "user.message")
                .map(|e| e["itemId"].clone())
                .collect::<Vec<_>>(),
            receipts[name]
                .iter()
                .map(|r| r["messageId"].clone())
                .collect::<Vec<_>>()
        );
        let last = history
            .iter()
            .rfind(|e| e["type"] == "agent.message")
            .unwrap();
        assert!(
            last["payload"]["content"]
                .to_string()
                .contains(tokens[name])
        );
        for (peer, token) in &tokens {
            if *peer != name {
                assert!(!json!(history).to_string().contains(token));
            }
        }
        h.assert_history(name).await;
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
        histories.insert(name, history);
    }
    let a: BTreeSet<_> = histories["cedar"]
        .iter()
        .map(|e| e["itemId"].as_str().unwrap())
        .collect();
    let b: BTreeSet<_> = histories["birch"]
        .iter()
        .map(|e| e["itemId"].as_str().unwrap())
        .collect();
    assert!(a.is_disjoint(&b));
    let transcript: Vec<Value> = std::fs::read_to_string(h.root.join("rpc.jsonl"))
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    let first_return = transcript
        .iter()
        .filter(|r| {
            r["method"] == "session/new"
                && matches!(r["params"]["sessionName"].as_str(), Some("cedar" | "birch"))
        })
        .map(|r| r["finishedAtNs"].as_u64().unwrap())
        .min()
        .unwrap();
    let mut host_count = 0;
    for entry in std::fs::read_dir(h.root.join("native"))
        .unwrap()
        .filter_map(Result::ok)
        .filter(|e| e.path().join("wire.jsonl").exists())
    {
        host_count += 1;
        let wire: Vec<Value> = std::fs::read_to_string(entry.path().join("wire.jsonl"))
            .unwrap()
            .lines()
            .map(|line| serde_json::from_str(line).unwrap())
            .collect();
        let initialized: Vec<_> = wire
            .iter()
            .filter(|r| r["direction"] == "local" && r["payload"]["method"] == "sdk/initialized")
            .collect();
        assert_eq!(initialized.len(), 1);
        assert_eq!(
            initialized[0]["payload"]["handshakeOwner"],
            "codex-app-server-client"
        );
        let spawn: Value =
            serde_json::from_slice(&std::fs::read(entry.path().join("spawn.json")).unwrap())
                .unwrap();
        assert_eq!(spawn["cwd"], "/");
        let expected: Vec<_> = sessions
            .values()
            .filter(|s| h.runtime(s)["pid"] == spawn["runtime"]["pid"])
            .collect();
        let opens: Vec<_> = wire
            .iter()
            .filter(|r| r["direction"] == "client" && r["payload"]["method"] == "thread/start")
            .collect();
        assert_eq!(
            opens
                .iter()
                .map(|r| r["payload"]["params"]["cwd"].as_str().unwrap())
                .collect::<BTreeSet<_>>(),
            expected
                .iter()
                .map(|r| r["spec"]["cwd"].as_str().unwrap())
                .collect::<BTreeSet<_>>()
        );
        if expected.len() == 2 {
            assert!(
                opens
                    .iter()
                    .all(|r| r["time"].as_str().unwrap().parse::<u64>().unwrap() < first_return),
                "both native opens must be sent before either creation returns"
            );
            let frames: Vec<_> = wire
                .iter()
                .filter(|r| r["direction"] == "server")
                .map(|r| &r["payload"])
                .collect();
            let starts: Vec<_> = frames
                .iter()
                .enumerate()
                .filter(|(_, r)| r["method"] == "turn/started")
                .map(|(i, _)| i)
                .collect();
            let ends: Vec<_> = frames
                .iter()
                .enumerate()
                .filter(|(_, r)| r["method"] == "turn/completed")
                .map(|(i, _)| i)
                .collect();
            assert!(starts[1] < ends[0]);
            let previews: Vec<_> = frames[..ends[0]]
                .iter()
                .filter(|r| r["method"] == "item/agentMessage/delta")
                .map(|r| r["params"]["threadId"].as_str().unwrap())
                .collect();
            assert_eq!(previews.iter().copied().collect::<BTreeSet<_>>().len(), 2);
            assert!(
                previews.windows(2).filter(|w| w[0] != w[1]).count() >= 2,
                "actual streaming must interleave"
            );
        }
    }
    assert_eq!(host_count, 2);
    std::fs::write(h.root.join("result.json"),json!({"passed":true,"migratedFrom":"codex_shared_threads.py","sessions":sessions,"receipts":receipts,"simultaneousRunning":true,"distinctStates":true}).to_string()).unwrap();
}
