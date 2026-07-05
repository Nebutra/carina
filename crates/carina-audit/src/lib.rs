//! Append-only, tamper-evident event log for Carina (PRD §8.2, §9.2).
//!
//! One JSONL file per session. Every event carries the language `actor`
//! that produced it and is chained by SHA-256: `event_hash =
//! sha256(prev_hash || canonical(event))`. Any insertion, deletion,
//! reordering, or field edit breaks the chain and is caught by [`verify`].

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::fs::OpenOptions;
use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use time::format_description::well_known::Rfc3339;
use time::OffsetDateTime;

/// The genesis hash that seeds every session's chain.
pub const GENESIS_HASH: &str = "0000000000000000000000000000000000000000000000000000000000000000";

/// The twenty canonical event types (PRD §8.2).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum EventType {
    TaskCreated,
    ModelRequested,
    ModelResponded,
    ToolRequested,
    ToolApproved,
    ToolDenied,
    FileRead,
    FileWriteProposed,
    PatchProposed,
    PatchApplied,
    PatchFailed,
    CommandStarted,
    CommandOutput,
    CommandExited,
    NetworkRequested,
    SecretRequested,
    PolicyViolation,
    RollbackStarted,
    RollbackCompleted,
    SessionClosed,
}

/// Which layer produced the event (PRD §4.1: the control flow must be
/// visibly Go → Rust → Zig in the audit trail).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "lowercase")]
pub enum Actor {
    /// Go control plane (task lifecycle, scheduling, sessions).
    Go,
    /// Rust capability kernel (policy decisions, patches, audit).
    #[default]
    Rust,
    /// Zig native toolchain (scan/grep/run/patch/pty execution).
    Zig,
    /// The LLM (model requests/responses).
    Model,
    /// A human (approvals/denials).
    User,
    /// The agent surface.
    Agent,
    /// A WASM plugin.
    Plugin,
}

impl Actor {
    pub fn from_str(s: &str) -> Actor {
        match s {
            "go" | "Go" => Actor::Go,
            "zig" | "Zig" => Actor::Zig,
            "model" | "Model" => Actor::Model,
            "user" | "User" => Actor::User,
            "agent" | "Agent" => Actor::Agent,
            "plugin" | "Plugin" => Actor::Plugin,
            _ => Actor::Rust,
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Event {
    pub event_id: String,
    pub session_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task_id: Option<String>,
    #[serde(rename = "type")]
    pub event_type: EventType,
    /// The layer that produced this event.
    #[serde(default)]
    pub actor: Actor,
    /// RFC 3339 timestamp.
    pub timestamp: String,
    #[serde(default)]
    pub payload: serde_json::Value,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub permission_decision_id: Option<String>,
    /// Hash of the previous event in this session's chain.
    #[serde(default)]
    pub prev_hash: String,
    /// `sha256(prev_hash || canonical(this event with event_hash cleared))`.
    #[serde(default)]
    pub event_hash: String,
}

impl Event {
    pub fn new(session_id: impl Into<String>, event_type: EventType, payload: serde_json::Value) -> Self {
        Self {
            event_id: new_event_id(),
            session_id: session_id.into(),
            task_id: None,
            event_type,
            actor: Actor::Rust,
            timestamp: OffsetDateTime::now_utc()
                .format(&Rfc3339)
                .unwrap_or_else(|_| "1970-01-01T00:00:00Z".into()),
            payload,
            permission_decision_id: None,
            prev_hash: String::new(),
            event_hash: String::new(),
        }
    }

    pub fn with_task(mut self, task_id: impl Into<String>) -> Self {
        self.task_id = Some(task_id.into());
        self
    }

    pub fn with_decision(mut self, decision_id: impl Into<String>) -> Self {
        self.permission_decision_id = Some(decision_id.into());
        self
    }

    pub fn with_actor(mut self, actor: Actor) -> Self {
        self.actor = actor;
        self
    }

    /// Computes this event's hash given the previous hash. The `event_hash`
    /// field is excluded from the hashed representation (it is the output).
    fn compute_hash(&self, prev_hash: &str) -> String {
        let mut probe = self.clone();
        probe.prev_hash = prev_hash.to_string();
        probe.event_hash = String::new();
        let canonical = serde_json::to_string(&probe).unwrap_or_default();
        let mut hasher = Sha256::new();
        hasher.update(prev_hash.as_bytes());
        hasher.update(canonical.as_bytes());
        format!("{:x}", hasher.finalize())
    }
}

#[derive(Debug, thiserror::Error)]
pub enum AuditError {
    #[error("audit io: {0}")]
    Io(#[from] std::io::Error),
    #[error("audit serialization: {0}")]
    Serde(#[from] serde_json::Error),
}

/// The result of verifying a session's hash chain.
#[derive(Debug, Clone, Serialize)]
pub struct VerifyReport {
    pub ok: bool,
    pub event_count: usize,
    /// Index of the first broken event, if any.
    pub broken_at: Option<usize>,
    pub reason: Option<String>,
    /// The final chain head (useful for external anchoring).
    pub head_hash: String,
}

/// Append-only, hash-chained JSONL audit log for a single session.
pub struct AuditLog {
    path: PathBuf,
    /// Chain head; the next appended event links to this.
    last_hash: Mutex<String>,
}

impl AuditLog {
    /// Opens (or creates) the log for `session_id` under `dir`, recovering
    /// the chain head from the existing file so appends continue the chain
    /// after a restart.
    pub fn open(dir: &Path, session_id: &str) -> Result<Self, AuditError> {
        std::fs::create_dir_all(dir)?;
        let log = Self {
            path: dir.join(format!("{session_id}.events.jsonl")),
            last_hash: Mutex::new(GENESIS_HASH.to_string()),
        };
        // Recover the chain head from the last event on disk.
        let events = log.read_all()?;
        if let Some(last) = events.last() {
            if !last.event_hash.is_empty() {
                *log.last_hash.lock().unwrap() = last.event_hash.clone();
            }
        }
        Ok(log)
    }

    /// Appends one event, linking it into the hash chain. The log is never
    /// rewritten or truncated.
    pub fn append(&self, event: &Event) -> Result<(), AuditError> {
        let mut head = self.last_hash.lock().unwrap();
        let prev = head.clone();
        let mut linked = event.clone();
        linked.prev_hash = prev.clone();
        linked.event_hash = linked.compute_hash(&prev);

        let mut file = OpenOptions::new().create(true).append(true).open(&self.path)?;
        let line = serde_json::to_string(&linked)?;
        file.write_all(line.as_bytes())?;
        file.write_all(b"\n")?;

        *head = linked.event_hash;
        Ok(())
    }

    /// Replays the full event stream in append order.
    pub fn read_all(&self) -> Result<Vec<Event>, AuditError> {
        if !self.path.exists() {
            return Ok(vec![]);
        }
        let reader = BufReader::new(std::fs::File::open(&self.path)?);
        let mut events = Vec::new();
        for line in reader.lines() {
            let line = line?;
            if line.trim().is_empty() {
                continue;
            }
            events.push(serde_json::from_str(&line)?);
        }
        Ok(events)
    }

    /// Recomputes the chain and reports the first tampering, if any
    /// (PRD §9.2: `carina audit verify` must detect tampering).
    pub fn verify(&self) -> Result<VerifyReport, AuditError> {
        let events = self.read_all()?;
        let mut prev = GENESIS_HASH.to_string();
        for (i, e) in events.iter().enumerate() {
            if e.prev_hash != prev {
                return Ok(VerifyReport {
                    ok: false,
                    event_count: events.len(),
                    broken_at: Some(i),
                    reason: Some(format!(
                        "chain break at event {i}: prev_hash {} != expected {}",
                        short(&e.prev_hash),
                        short(&prev)
                    )),
                    head_hash: prev,
                });
            }
            let recomputed = e.compute_hash(&prev);
            if recomputed != e.event_hash {
                return Ok(VerifyReport {
                    ok: false,
                    event_count: events.len(),
                    broken_at: Some(i),
                    reason: Some(format!("event {i} content tampered (hash mismatch)")),
                    head_hash: prev,
                });
            }
            prev = e.event_hash.clone();
        }
        Ok(VerifyReport {
            ok: true,
            event_count: events.len(),
            broken_at: None,
            reason: None,
            head_hash: prev,
        })
    }

    pub fn path(&self) -> &Path {
        &self.path
    }
}

fn short(h: &str) -> String {
    h.chars().take(12).collect()
}

fn new_event_id() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or_default();
    format!("evt_{nanos:x}")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp(name: &str) -> PathBuf {
        use std::time::{SystemTime, UNIX_EPOCH};
        let n = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
        std::env::temp_dir().join(format!("carina-audit-{name}-{}-{n:x}", std::process::id()))
    }

    #[test]
    fn append_then_replay_roundtrip() {
        let dir = tmp("roundtrip");
        let log = AuditLog::open(&dir, "sess_test").unwrap();
        let ev = Event::new("sess_test", EventType::TaskCreated, serde_json::json!({"task_id": "task_1"}));
        log.append(&ev).unwrap();
        let events = log.read_all().unwrap();
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].event_type, EventType::TaskCreated);
        assert!(!events[0].event_hash.is_empty());
        assert_eq!(events[0].prev_hash, GENESIS_HASH);
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn intact_chain_verifies() {
        let dir = tmp("intact");
        let log = AuditLog::open(&dir, "s").unwrap();
        for i in 0..5 {
            log.append(&Event::new("s", EventType::FileRead, serde_json::json!({"i": i}))).unwrap();
        }
        let report = log.verify().unwrap();
        assert!(report.ok, "{:?}", report.reason);
        assert_eq!(report.event_count, 5);
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn tampering_a_payload_breaks_the_chain() {
        let dir = tmp("tamper");
        std::fs::create_dir_all(&dir).unwrap();
        let log = AuditLog::open(&dir, "s").unwrap();
        for i in 0..4 {
            log.append(&Event::new("s", EventType::CommandStarted, serde_json::json!({"cmd": i}))).unwrap();
        }
        // Rewrite the file with a mutated middle event.
        let mut events = log.read_all().unwrap();
        events[2].payload = serde_json::json!({"cmd": "EVIL"});
        let mut out = String::new();
        for e in &events {
            out.push_str(&serde_json::to_string(e).unwrap());
            out.push('\n');
        }
        std::fs::write(log.path(), out).unwrap();

        let report = log.verify().unwrap();
        assert!(!report.ok);
        assert_eq!(report.broken_at, Some(2));
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn deleting_an_event_breaks_the_chain() {
        let dir = tmp("delete");
        let log = AuditLog::open(&dir, "s").unwrap();
        for i in 0..4 {
            log.append(&Event::new("s", EventType::FileRead, serde_json::json!({"i": i}))).unwrap();
        }
        let mut events = log.read_all().unwrap();
        events.remove(1); // drop an event
        let mut out = String::new();
        for e in &events {
            out.push_str(&serde_json::to_string(e).unwrap());
            out.push('\n');
        }
        std::fs::write(log.path(), out).unwrap();
        assert!(!log.verify().unwrap().ok);
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn chain_survives_reopen() {
        let dir = tmp("reopen");
        {
            let log = AuditLog::open(&dir, "s").unwrap();
            log.append(&Event::new("s", EventType::TaskCreated, serde_json::json!({}))).unwrap();
        }
        // Reopen and append more; the chain must stay valid.
        let log = AuditLog::open(&dir, "s").unwrap();
        log.append(&Event::new("s", EventType::SessionClosed, serde_json::json!({}))).unwrap();
        let report = log.verify().unwrap();
        assert!(report.ok, "{:?}", report.reason);
        assert_eq!(report.event_count, 2);
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn actor_is_recorded() {
        let dir = tmp("actor");
        let log = AuditLog::open(&dir, "s").unwrap();
        log.append(&Event::new("s", EventType::TaskCreated, serde_json::json!({})).with_actor(Actor::Go)).unwrap();
        let events = log.read_all().unwrap();
        assert_eq!(events[0].actor, Actor::Go);
        std::fs::remove_dir_all(&dir).ok();
    }
}
