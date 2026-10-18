use serde_json::{Value, json};
use std::collections::BTreeMap;

pub fn table() -> BTreeMap<u32, Value> {
    let output = std::process::Command::new("ps")
        .args(["-axo", "pid=,ppid=,lstart=,comm="])
        .env("LC_ALL", "C")
        .output()
        .expect("read owned process identities");
    assert!(output.status.success(), "ps identity readback unavailable");
    String::from_utf8(output.stdout)
        .unwrap()
        .lines()
        .filter_map(|line| {
            let fields: Vec<_> = line.split_whitespace().collect();
            if fields.len() < 8 {
                return None;
            }
            let pid = fields[0].parse::<u32>().unwrap();
            Some((
                pid,
                json!({
                    "pid": pid,
                    "ppid": fields[1].parse::<u32>().unwrap(),
                    "start": fields[2..7].join(" "),
                    "command": fields[7..].join(" ")
                }),
            ))
        })
        .collect()
}
pub fn descendants(table: &BTreeMap<u32, Value>, root: u32) -> BTreeMap<u32, Value> {
    let mut owned = BTreeMap::new();
    if let Some(row) = table.get(&root) {
        owned.insert(root, row.clone());
    }
    loop {
        let found: Vec<_> = table
            .iter()
            .filter(|(pid, row)| {
                !owned.contains_key(pid)
                    && owned.contains_key(&(row["ppid"].as_u64().unwrap() as u32))
            })
            .map(|(pid, row)| (*pid, row.clone()))
            .collect();
        if found.is_empty() {
            break;
        }
        owned.extend(found);
    }
    owned
}
