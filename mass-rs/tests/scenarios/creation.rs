use crate::support::Native;
use serde_json::{Value, json};
use std::{
    collections::BTreeSet,
    path::{Path, PathBuf},
    time::{Duration, Instant},
};

fn directories(path: &Path) -> BTreeSet<PathBuf> {
    std::fs::read_dir(path)
        .unwrap()
        .map(|entry| entry.unwrap().path())
        .collect()
}
async fn create(h: &Native, name: &str, cwd: &Path) -> Value {
    h.call(
        "session/new",
        name,
        json!({"spec":{"runtimeName":"codex","cwd":cwd}}),
    )
    .await
}
pub fn version_config() -> Value {
    let output = std::process::Command::new("/usr/bin/shasum")
        .args(["-a", "256", "/usr/bin/true"])
        .output()
        .unwrap();
    assert!(output.status.success());
    let digest = String::from_utf8(output.stdout)
        .unwrap()
        .split_whitespace()
        .next()
        .unwrap()
        .to_owned();
    json!({"executable":"/usr/bin/true","sha256":digest})
}

pub async fn shared(h: &Native, scenario: &str) {
    let healthy = h.new_session("healthy").await;
    let provisional = h.root.join("provisional");
    std::fs::create_dir(&provisional).unwrap();
    if scenario == "open" {
        std::fs::create_dir(provisional.join(".codex")).unwrap();
        std::fs::write(provisional.join(".codex/config.toml"), "model = [\n").unwrap();
    }
    let data = h.root.join("data/sessions");
    let existing = directories(&data);
    let pending = create(h, "provisional", &provisional);
    let inspect = async {
        // A scheduled request delay gives the actual HTTP operation ownership first.
        tokio::time::sleep(Duration::from_millis(100)).await;
        assert_eq!(
            directories(&data),
            existing,
            "no storage identity exists before the Agent reply"
        );
        let started = Instant::now();
        assert_eq!(
            h.result("session/status", "healthy", json!({})).await["connected"],
            true
        );
        let duplicate = create(h, "provisional", &h.root.join("healthy")).await;
        assert!(
            duplicate["error"]["message"]
                .as_str()
                .unwrap()
                .contains("already exists")
        );
        assert!(
            started.elapsed() < Duration::from_millis(800),
            "native open must not hold the service map lock"
        );
        if scenario == "late" {
            let receipt = h
                .prompt(
                    "healthy",
                    "Without tools, reply with only MASS_DURING_OPEN_17362.",
                )
                .await;
            assert_eq!(receipt["acceptance"], "native_recorded");
            assert!(
                !h.frames("thread/closed")
                    .iter()
                    .any(|r| r["params"]["threadId"] != healthy["sessionId"])
            );
            assert!(
                started.elapsed() < Duration::from_secs(30),
                "healthy input must settle before the delayed open outcome"
            );
        }
    };
    let inject = async {
        if scenario == "metadata" {
            Some(metadata_blocker(h).await)
        } else {
            None
        }
    };
    let (failure, (), blocker) = tokio::join!(pending, inspect, inject);
    if let Some(directory) = blocker {
        assert!(
            directory.join("metadata.json").is_dir(),
            "foreign failure fixture must be preserved"
        );
        std::fs::remove_dir_all(directory).unwrap();
    }
    assert_eq!(failure["error"]["code"], -32603, "{failure}");
    if scenario == "late" {
        assert!(failure["error"]["data"]["kind"] == "open_unknown");
        assert!(
            create(h, "provisional", &h.root.join("healthy")).await["error"]["message"]
                .as_str()
                .unwrap()
                .contains("already exists")
        );
    }
    assert_eq!(
        h.result("session/list", "", json!({}))
            .await
            .as_array()
            .unwrap()
            .iter()
            .map(|r| r["sessionName"].as_str().unwrap())
            .collect::<Vec<_>>(),
        vec!["healthy"]
    );
    assert_eq!(h.result("status", "", json!({})).await["connected"], 1);
    assert_eq!(directories(&data), existing);
    let opens = h.frames("thread/start");
    assert_eq!(opens.len(), 2);
    let response = h
        .wait(|| {
            h.wire()
                .as_array()
                .unwrap()
                .iter()
                .find(|r| {
                    r["direction"] == "server"
                        && r["payload"]["id"] == opens[1]["id"]
                        && r["payload"].get("method").is_none()
                })
                .map(|r| r["payload"].clone())
        })
        .await;
    if scenario == "open" {
        assert!(response.get("error").is_some());
        assert!(h.frames("thread/unsubscribe").is_empty());
    } else {
        let id = &response["result"]["thread"]["id"];
        assert_ne!(*id, healthy["sessionId"]);
        h.wait(|| {
            h.frames("thread/closed")
                .iter()
                .any(|r| r["params"]["threadId"] == *id)
                .then_some(())
        })
        .await;
        assert!(
            h.frames("thread/unsubscribe")
                .iter()
                .any(|r| r["params"]["threadId"] == *id)
        );
    }
    if scenario == "late" {
        h.idle("healthy").await;
    }
    h.prompt(
        "healthy",
        "Without tools, reply with only MASS_SHARED_SURVIVED_73591.",
    )
    .await;
    h.idle("healthy").await;
    let history = h.history("healthy").await;
    assert!(history.iter().any(|e| {
        e["type"] == "agent.message"
            && e["payload"]
                .to_string()
                .contains("MASS_SHARED_SURVIVED_73591")
    }));
    assert!(
        history
            .iter()
            .all(|e| h.private_payload(e)["source"]["threadId"] == healthy["sessionId"])
    );
    h.assert_history("healthy").await;
    assert_eq!(spawn_count(h), 1);
    let replacement = create(h, "provisional", &h.root.join("healthy")).await;
    assert_eq!(
        h.runtime(&replacement["result"])["pid"],
        h.runtime(&healthy)["pid"],
        "{replacement}"
    );
    assert_eq!(h.result("status", "", json!({})).await["connected"], 2);
    std::fs::write(h.root.join("result.json"),serde_json::to_vec_pretty(&json!({"passed":true,"migratedFrom":"codex_shared_failures.py","scenario":scenario,"failure":failure,"replacement":replacement})).unwrap()).unwrap();
}
fn spawn_count(h: &Native) -> usize {
    std::fs::read_dir(h.root.join("native"))
        .map(|entries| {
            entries
                .filter_map(Result::ok)
                .filter(|e| e.path().join("spawn.json").exists())
                .count()
        })
        .unwrap_or(0)
}

pub async fn single(h: &Native, scenario: &str) {
    let cwd = h.root.join("target");
    std::fs::create_dir(&cwd).unwrap();
    let pending = create(h, "target", &cwd);
    let inject = async {
        if scenario == "metadata" {
            Some(metadata_blocker(h).await)
        } else {
            None
        }
    };
    let (failure, blocker) = tokio::join!(pending, inject);
    if let Some(directory) = blocker {
        assert!(directory.join("metadata.json").is_dir());
        std::fs::remove_dir_all(directory).unwrap();
    }
    assert_eq!(failure["error"]["code"], -32603, "{failure}");
    assert_eq!(h.result("session/list", "", json!({})).await, json!([]));
    assert!(directories(&h.root.join("data/sessions")).is_empty());
    match scenario {
        "version" => {
            assert!(failure["error"]["data"]["kind"] == "runtime_unavailable");
            assert_eq!(spawn_count(h), 0);
        }
        "open_timeout" => {
            assert!(failure["error"]["data"]["kind"] == "open_unknown");
            assert!(
                create(h, "target", &cwd).await["error"]["message"]
                    .as_str()
                    .unwrap()
                    .contains("already exists")
            );
            h.wait_wire("thread/closed").await;
        }
        "metadata" => {
            h.wait_wire("thread/closed").await;
        }
        _ => panic!("unknown creation scenario"),
    }
    assert!(h.frames("turn/start").is_empty());
    std::fs::write(h.root.join("result.json"),serde_json::to_vec_pretty(&json!({"passed":true,"migratedFrom":"codex_text_failures.py","scenario":scenario,"failure":failure})).unwrap()).unwrap();
}

async fn metadata_blocker(h: &Native) -> PathBuf {
    // Observe a real successful native start response while the adapter's test-only
    // response delivery delay holds it. Never invent an Agent identity.
    let opened = h
        .wait(|| {
            h.wire()
                .as_array()
                .unwrap()
                .iter()
                .rev()
                .find(|row| {
                    row["direction"] == "server"
                        && row["payload"]["result"]["thread"]["id"].is_string()
                        && row["payload"]["result"]["thread"]["cwd"]
                            != h.root.join("healthy").to_string_lossy().as_ref()
                })
                .map(|row| row["payload"]["result"]["thread"].clone())
        })
        .await;
    let session = json!({"sessionId":opened["id"],"spec":{"runtimeName":"codex"}});
    let directory = h.session_dir(&session);
    std::fs::create_dir(&directory).unwrap();
    std::fs::create_dir(directory.join("metadata.json")).unwrap();
    directory
}
