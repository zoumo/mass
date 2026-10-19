//! Explicit native acceptance orchestrator. All assertions live in Rust tests.
#[path = "support/process.rs"]
mod process;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeMap,
    io::{BufRead, BufReader, Write},
    path::{Path, PathBuf},
    process::{Command, Stdio},
};
fn hashes(repo: &Path) -> BTreeMap<String, String> {
    let output = Command::new("git")
        .current_dir(repo)
        .args([
            "ls-files",
            "--cached",
            "--others",
            "--exclude-standard",
            "-z",
            "--",
            "mass-rs",
            "Makefile",
            "harness",
        ])
        .output()
        .unwrap();
    assert!(output.status.success());
    output
        .stdout
        .split(|b| *b == 0)
        .filter(|b| !b.is_empty())
        .filter_map(|path| {
            let path = String::from_utf8(path.to_vec()).unwrap();
            let absolute = repo.join(&path);
            absolute.is_file().then(|| {
                (
                    path,
                    format!("{:x}", Sha256::digest(std::fs::read(absolute).unwrap())),
                )
            })
        })
        .collect()
}
fn files(root: &Path, name: &str, out: &mut Vec<PathBuf>) {
    for entry in std::fs::read_dir(root).unwrap() {
        let entry = entry.unwrap();
        if entry.file_type().unwrap().is_dir() {
            files(&entry.path(), name, out);
        } else if entry.file_name().to_string_lossy().ends_with(name) {
            out.push(entry.path());
        }
    }
}
fn executable(artifacts: &[Value], target: &str) -> PathBuf {
    let paths: std::collections::BTreeSet<_> = artifacts
        .iter()
        .filter(|row| {
            row["reason"] == "compiler-artifact"
                && row["target"]["name"] == target
                && row["profile"]["test"] == true
        })
        .filter_map(|row| row["executable"].as_str())
        .collect();
    assert_eq!(
        paths.len(),
        1,
        "exactly one compiled {target} artifact required"
    );
    PathBuf::from(paths.first().unwrap())
}
fn test_command(artifacts: &[Value], target: &str, filter: &str) -> Command {
    let mut command = Command::new(executable(artifacts, target));
    if !filter.is_empty() {
        command.arg(filter);
    }
    command
}
#[test]
#[ignore = "explicit real native matrix; make test-native; reviewed Codex and release required"]
fn native_acceptance_matrix() {
    for key in ["MASS_TEST_CODEX", "MASS_TEST_CODEX_SHA256"] {
        assert!(std::env::var_os(key).is_some(), "{key} required");
    }
    let repo = Path::new(env!("CARGO_MANIFEST_DIR")).parent().unwrap();
    let artifact_path = std::env::var_os("MASS_TEST_ARTIFACTS")
        .expect("use make test-native to compile and inventory native test executables");
    let artifact_bytes = std::fs::read(artifact_path).unwrap();
    let artifacts: Vec<Value> = String::from_utf8(artifact_bytes.clone())
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    let filter = std::env::var("MASS_TEST_FILTER").unwrap_or_default();
    if filter.is_empty() {
        let binary = PathBuf::from(
            std::env::var_os("MASS_TEST_MASSD")
                .expect("full native matrix requires MASS_TEST_MASSD release executable"),
        );
        assert!(binary.is_absolute() && binary.is_file());
        assert!(
            binary.parent().unwrap().join("manifest.json").is_file(),
            "release manifest required"
        );
    }
    let base = std::env::var_os("MASS_TEST_EVIDENCE")
        .map(PathBuf::from)
        .unwrap_or_else(std::env::temp_dir);
    std::fs::create_dir_all(&base).unwrap();
    let root = tempfile::Builder::new()
        .prefix("mass-native-stage-")
        .tempdir_in(base)
        .unwrap()
        .keep();
    eprintln!("native stage evidence: {}", root.display());
    std::fs::write(root.join("test-artifacts.jsonl"), artifact_bytes).unwrap();
    let before = hashes(repo);
    std::fs::write(
        root.join("source-manifest.json"),
        serde_json::to_vec_pretty(&before).unwrap(),
    )
    .unwrap();
    let mut rows = Vec::new();
    let mut total = 0;
    for (package, target, default_filter) in [
        ("mass-codex", "native", ""),
        ("mass-daemon", "native_codex", "migrated_"),
        ("mass-daemon", "http_health", "migrated_"),
    ] {
        let selected = if filter.is_empty() {
            default_filter
        } else {
            &filter
        };
        let list = test_command(&artifacts, target, selected)
            .args(["--ignored", "--list"])
            .output()
            .unwrap();
        assert!(
            list.status.success(),
            "{}",
            String::from_utf8_lossy(&list.stderr)
        );
        let names: Vec<_> = String::from_utf8(list.stdout)
            .unwrap()
            .lines()
            .filter_map(|l| l.strip_suffix(": test").map(str::to_owned))
            .collect();
        if names.is_empty() {
            continue;
        }
        eprintln!("native stage: {package}/{target}: {} cases", names.len());
        let group = root.join(target);
        std::fs::create_dir(&group).unwrap();
        let mut command = test_command(&artifacts, target, selected);
        command
            .args(["--ignored", "--test-threads=1", "--nocapture"])
            .env("MASS_TEST_EVIDENCE", &group)
            .env("RUST_MIN_STACK", "16777216")
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        let mut child = command.spawn().unwrap();
        let readers: Vec<_> = [
            (
                "stdout.log",
                child
                    .stdout
                    .take()
                    .map(|v| Box::new(v) as Box<dyn std::io::Read + Send>),
            ),
            (
                "stderr.log",
                child
                    .stderr
                    .take()
                    .map(|v| Box::new(v) as Box<dyn std::io::Read + Send>),
            ),
        ]
        .into_iter()
        .map(|(name, reader)| {
            let path = group.join(name);
            std::thread::spawn(move || {
                let mut log = std::fs::File::create(path).unwrap();
                let mut text = String::new();
                for line in BufReader::new(reader.unwrap()).lines() {
                    let line = line.unwrap();
                    writeln!(log, "{line}").unwrap();
                    eprintln!("{line}");
                    text.push_str(&line);
                    text.push('\n');
                }
                text
            })
        })
        .collect();
        let status = child.wait().unwrap();
        let output = readers
            .into_iter()
            .map(|r| r.join().unwrap())
            .collect::<Vec<_>>()
            .join("\n");
        let passed = status.success()
            && output.contains(&format!(
                "test result: ok. {} passed; 0 failed;",
                names.len()
            ));
        rows.push(json!({"package":package,"target":target,"tests":names,"passed":passed,"exitCode":status.code()}));
        total += names.len();
        std::fs::write(
            root.join("matrix.json"),
            serde_json::to_vec_pretty(&rows).unwrap(),
        )
        .unwrap();
        if !passed {
            break;
        }
    }
    assert!(total > 0, "filter matched no native test: {filter}");
    let mut cleanup = Vec::new();
    files(&root, "cleanup.json", &mut cleanup);
    let mut owned = BTreeMap::new();
    for path in cleanup {
        let value: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        if let Some(remaining) = value.get("remainingOwned") {
            assert_eq!(*remaining, json!([]), "{}", path.display());
            for key in ["ownedBeforeShutdown", "observedOwned"] {
                if let Some(rows) = value[key].as_array() {
                    for row in rows {
                        owned.insert(
                            (
                                row["pid"].as_u64().unwrap() as u32,
                                row["start"].as_str().unwrap().to_owned(),
                            ),
                            row.clone(),
                        );
                    }
                }
            }
        } else if value.get("reaped").is_some() {
            assert_eq!(value["reaped"], true, "{}", path.display());
        }
    }
    let table = process::table();
    let mut remaining_trees = BTreeMap::new();
    for (pid, start) in owned.keys() {
        if table.get(pid).is_some_and(|row| row["start"] == *start) {
            remaining_trees.extend(process::descendants(&table, *pid));
        }
    }
    let remaining: Vec<_> = owned
        .iter()
        .filter(|((pid, start), _)| table.get(pid).is_some_and(|row| row["start"] == *start))
        .map(|(_, row)| row.clone())
        .collect();
    std::fs::write(
        root.join("process-readback.json"),
        json!({"observedIdentities":owned.len(),"remainingOwned":remaining}).to_string(),
    )
    .unwrap();
    let source_unchanged = hashes(repo) == before;
    let passed = rows.iter().all(|row| row["passed"] == true)
        && remaining.is_empty()
        && remaining_trees.is_empty()
        && source_unchanged;
    std::fs::write(root.join("result.json"),json!({"passed":passed,"rustTest":"native_acceptance_matrix","cases":total,"groups":rows.len(),"filter":filter,"sourceUnchanged":source_unchanged}).to_string()).unwrap();
    assert!(passed, "native matrix failed; see {}", root.display());
}
