//! Durable instance identity. Configuration files select records; omissions never delete them.
use anyhow::{Result, ensure};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    collections::{BTreeMap, BTreeSet},
    io::Write,
    path::Path,
};

#[derive(Clone)]
pub struct Registration {
    pub name: String,
    pub runtime_type: String,
    pub fingerprint: String,
    pub enabled: bool,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Record {
    identity: String,
    runtime_type: String,
    fingerprint: String,
    version: u64,
}
#[derive(Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Ledger {
    format_version: u32,
    records: BTreeMap<String, Record>,
}
pub(super) struct Registry {
    ledger: Ledger,
    pub configured: BTreeMap<String, Registration>,
    // One daemon owns a data directory. Keep the OS lock through Service shutdown/drop.
    lock: std::sync::Mutex<Option<std::fs::File>>,
}
impl Registry {
    pub fn load(directory: &Path, registrations: Vec<Registration>) -> Result<Self> {
        std::fs::create_dir_all(directory)?;
        let lock = std::fs::OpenOptions::new()
            .create(true)
            .truncate(false)
            .read(true)
            .write(true)
            .open(directory.join("runtime-registry.lock"))?;
        lock.try_lock()
            .map_err(|_| anyhow::anyhow!("runtime data directory is already owned"))?;
        let path = directory.join("runtime-registry.json");
        let original = match std::fs::read(&path) {
            Ok(bytes) => Some(bytes),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => None,
            Err(e) => return Err(e.into()),
        };
        let existing = original.is_some();
        let mut ledger = if existing {
            serde_json::from_slice::<Ledger>(original.as_ref().unwrap())?
        } else {
            Ledger {
                format_version: 1,
                records: BTreeMap::new(),
            }
        };
        ensure!(
            ledger.format_version == 1,
            "unsupported runtime registry format"
        );
        let mut identities = BTreeSet::new();
        for (name, record) in &ledger.records {
            ensure!(
                !name.is_empty()
                    && uuid::Uuid::parse_str(&record.identity).is_ok()
                    && identities.insert(&record.identity)
                    && record.version > 0
                    && !record.runtime_type.is_empty()
                    && record.fingerprint.len() == 64
                    && record.fingerprint.bytes().all(|b| b.is_ascii_hexdigit()),
                "corrupt runtime registry"
            );
        }
        // Reuse the canonical format/path validation before any ledger write.
        let mut references = BTreeSet::new();
        for metadata in mass_journal::load(directory)?.values() {
            let name = &metadata.spec.runtime_name;
            let record = ledger
                .records
                .get(name)
                .ok_or_else(|| anyhow::anyhow!("session runtime identity is missing"))?;
            ensure!(
                metadata.instance == Self::binding(record),
                "session runtime binding mismatch"
            );
            references.insert(name.clone());
        }
        let mut configured = BTreeMap::new();
        for registration in registrations {
            ensure!(
                !configured.contains_key(&registration.name),
                "duplicate runtime name"
            );
            if let Some(record) = ledger.records.get_mut(&registration.name) {
                ensure!(
                    record.runtime_type == registration.runtime_type,
                    "runtime type is immutable"
                );
                if record.fingerprint != registration.fingerprint {
                    ensure!(
                        !references.contains(&registration.name),
                        "runtime configuration has session references"
                    );
                    record.fingerprint = registration.fingerprint.clone();
                    record.version = record
                        .version
                        .checked_add(1)
                        .ok_or_else(|| anyhow::anyhow!("runtime version exhausted"))?;
                }
            } else {
                ledger.records.insert(
                    registration.name.clone(),
                    Record {
                        identity: uuid::Uuid::now_v7().to_string(),
                        runtime_type: registration.runtime_type.clone(),
                        fingerprint: registration.fingerprint.clone(),
                        version: 1,
                    },
                );
            }
            configured.insert(registration.name.clone(), registration);
        }
        // Configuration/reference validation precedes ledger writes. The lock is
        // already held; also reject external replacement while validating.
        if existing {
            ensure!(
                Some(std::fs::read(&path)?) == original,
                "runtime registry changed during startup"
            );
        } else {
            ensure!(!path.exists(), "runtime registry changed during startup");
        }
        let bytes = serde_json::to_vec(&ledger)?;
        if !existing || std::fs::read(&path)? != bytes {
            let mut file = tempfile::NamedTempFile::new_in(directory)?;
            file.write_all(&bytes)?;
            file.as_file().sync_all()?;
            file.persist(path)?;
            std::fs::File::open(directory)?.sync_all()?;
        }
        Ok(Self {
            ledger,
            configured,
            lock: std::sync::Mutex::new(Some(lock)),
        })
    }
    pub fn release_lock(&self) {
        self.lock.lock().unwrap().take();
    }
    fn binding(record: &Record) -> Value {
        json!({"identity":record.identity,"type":record.runtime_type,"version":record.version})
    }
    pub fn get_binding(&self, name: &str) -> Value {
        self.ledger
            .records
            .get(name)
            .map(Self::binding)
            .unwrap_or(Value::Null)
    }
    pub fn enabled(&self, name: &str) -> bool {
        self.configured.get(name).is_some_and(|r| r.enabled)
    }
}
