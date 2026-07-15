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

/// Canonical event types (PRD §8.2).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum EventType {
    TaskCreated,
    ModelRequested,
    ModelResponded,
    RoutingDecision,
    RoutingOutcome,
    RoutingRetryScheduled,
    ContextCompacted,
    MemoryRecalled,
    MemoryProjectionChanged,
    MemoryProjected,
    ScheduleChanged,
    ScheduleTriggered,
    ToolRequested,
    ToolApproved,
    ToolDenied,
    ToolCallRequested,
    ToolCallApprovalRequired,
    ToolCallStarted,
    ToolCallCompleted,
    ToolCallFailed,
    ToolCallDenied,
    ToolCallCancelled,
    RuntimeStageChanged,
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
    SessionPaused,
    SessionResumed,
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
    /// A human operator acting through a governed client.
    Operator,
    /// A remote worker reporting governed execution state.
    Worker,
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
            "operator" | "Operator" => Actor::Operator,
            "worker" | "Worker" => Actor::Worker,
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
    pub fn new(
        session_id: impl Into<String>,
        event_type: EventType,
        payload: serde_json::Value,
    ) -> Self {
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
    #[error("audit log is poisoned after a failed append; reopen it before retrying")]
    Poisoned,
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
    /// Chain state and the file handle share one append critical section.
    state: Mutex<AuditState>,
}

struct AuditState {
    last_hash: String,
    event_count: usize,
    file: std::fs::File,
    poisoned: bool,
    #[cfg(test)]
    fail_next_persist: bool,
}

impl AuditLog {
    /// Opens (or creates) the log for `session_id` under `dir`, recovering
    /// the chain head from the existing file so appends continue the chain
    /// after a restart.
    pub fn open(dir: &Path, session_id: &str) -> Result<Self, AuditError> {
        std::fs::create_dir_all(dir)?;
        let path = dir.join(format!("{session_id}.events.jsonl"));
        // Recover the chain head before opening the persistent append handle.
        let events = read_all_from(&path)?;
        let mut last_hash = GENESIS_HASH.to_string();
        if let Some(last) = events.last() {
            if !last.event_hash.is_empty() {
                last_hash = last.event_hash.clone();
            }
        }
        let file = OpenOptions::new().create(true).append(true).open(&path)?;
        // Retrying open must also retry directory durability if an earlier
        // attempt created the file but failed before syncing its directory.
        sync_parent_dir(&path)?;
        Ok(Self {
            path,
            state: Mutex::new(AuditState {
                last_hash,
                event_count: events.len(),
                file,
                poisoned: false,
                #[cfg(test)]
                fail_next_persist: false,
            }),
        })
    }

    /// Appends one event, linking it into the hash chain. The log is never
    /// rewritten or truncated.
    pub fn append(&self, event: &Event) -> Result<(), AuditError> {
        self.append_with_cursor(event).map(|_| ())
    }

    /// Appends one event and returns the exclusive raw audit cursor (the
    /// number of durable events after this append) — "durable" now actually
    /// means fsynced, not merely handed to the OS's write() buffer.
    pub fn append_with_cursor(&self, event: &Event) -> Result<usize, AuditError> {
        let mut state = self.state.lock().unwrap();
        if state.poisoned {
            return Err(AuditError::Poisoned);
        }
        let prev = state.last_hash.clone();
        let mut linked = event.clone();
        linked.prev_hash = prev.clone();
        linked.event_hash = linked.compute_hash(&prev);

        let line = serde_json::to_string(&linked)?;
        #[cfg(test)]
        let persisted = if std::mem::take(&mut state.fail_next_persist) {
            Err(std::io::Error::other("injected audit persist failure"))
        } else {
            persist_line(&mut state.file, &line)
        };
        #[cfg(not(test))]
        let persisted = persist_line(&mut state.file, &line);
        if let Err(error) = persisted {
            // A failed write or fsync can leave unknown bytes on disk. Do not
            // compute another event from a stale in-memory chain head.
            state.poisoned = true;
            return Err(error.into());
        }

        state.last_hash = linked.event_hash;
        state.event_count += 1;
        Ok(state.event_count)
    }

    /// Replays the full event stream in append order.
    pub fn read_all(&self) -> Result<Vec<Event>, AuditError> {
        read_all_from(&self.path)
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

fn persist_line(file: &mut std::fs::File, line: &str) -> Result<(), std::io::Error> {
    file.write_all(line.as_bytes())?;
    file.write_all(b"\n")?;
    file.sync_all()
}

#[cfg(unix)]
fn sync_parent_dir(path: &Path) -> Result<(), std::io::Error> {
    if let Some(parent) = path.parent() {
        std::fs::File::open(parent)?.sync_all()?;
    }
    Ok(())
}

#[cfg(not(unix))]
fn sync_parent_dir(_path: &Path) -> Result<(), std::io::Error> {
    Ok(())
}

fn read_all_from(path: &Path) -> Result<Vec<Event>, AuditError> {
    if !path.exists() {
        return Ok(vec![]);
    }
    let reader = BufReader::new(std::fs::File::open(path)?);
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
        let n = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        std::env::temp_dir().join(format!("carina-audit-{name}-{}-{n:x}", std::process::id()))
    }

    #[test]
    fn append_then_replay_roundtrip() {
        let dir = tmp("roundtrip");
        let log = AuditLog::open(&dir, "sess_test").unwrap();
        let ev = Event::new(
            "sess_test",
            EventType::TaskCreated,
            serde_json::json!({"task_id": "task_1"}),
        );
        log.append(&ev).unwrap();
        let events = log.read_all().unwrap();
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].event_type, EventType::TaskCreated);
        assert!(!events[0].event_hash.is_empty());
        assert_eq!(events[0].prev_hash, GENESIS_HASH);
        drop(log);
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn append_cursor_is_monotonic_and_survives_reopen() {
        let dir = tmp("cursor");
        let log = AuditLog::open(&dir, "cursor").unwrap();
        let one = Event::new("cursor", EventType::TaskCreated, serde_json::json!({}));
        let two = Event::new("cursor", EventType::TaskCreated, serde_json::json!({}));
        assert_eq!(log.append_with_cursor(&one).unwrap(), 1);
        assert_eq!(log.append_with_cursor(&two).unwrap(), 2);
        drop(log);

        let reopened = AuditLog::open(&dir, "cursor").unwrap();
        let three = Event::new("cursor", EventType::TaskCreated, serde_json::json!({}));
        assert_eq!(reopened.append_with_cursor(&three).unwrap(), 3);
        drop(reopened);
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn intact_chain_verifies() {
        let dir = tmp("intact");
        let log = AuditLog::open(&dir, "s").unwrap();
        for i in 0..5 {
            log.append(&Event::new(
                "s",
                EventType::FileRead,
                serde_json::json!({"i": i}),
            ))
            .unwrap();
        }
        let report = log.verify().unwrap();
        assert!(report.ok, "{:?}", report.reason);
        assert_eq!(report.event_count, 5);
        drop(log);
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn tampering_a_payload_breaks_the_chain() {
        let dir = tmp("tamper");
        std::fs::create_dir_all(&dir).unwrap();
        let log = AuditLog::open(&dir, "s").unwrap();
        for i in 0..4 {
            log.append(&Event::new(
                "s",
                EventType::CommandStarted,
                serde_json::json!({"cmd": i}),
            ))
            .unwrap();
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
        drop(log);
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn deleting_an_event_breaks_the_chain() {
        let dir = tmp("delete");
        let log = AuditLog::open(&dir, "s").unwrap();
        for i in 0..4 {
            log.append(&Event::new(
                "s",
                EventType::FileRead,
                serde_json::json!({"i": i}),
            ))
            .unwrap();
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
        drop(log);
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn chain_survives_reopen() {
        let dir = tmp("reopen");
        {
            let log = AuditLog::open(&dir, "s").unwrap();
            log.append(&Event::new(
                "s",
                EventType::TaskCreated,
                serde_json::json!({}),
            ))
            .unwrap();
        }
        // Reopen and append more; the chain must stay valid.
        let log = AuditLog::open(&dir, "s").unwrap();
        log.append(&Event::new(
            "s",
            EventType::SessionClosed,
            serde_json::json!({}),
        ))
        .unwrap();
        let report = log.verify().unwrap();
        assert!(report.ok, "{:?}", report.reason);
        assert_eq!(report.event_count, 2);
        drop(log);
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn actor_is_recorded() {
        let dir = tmp("actor");
        let log = AuditLog::open(&dir, "s").unwrap();
        log.append(
            &Event::new("s", EventType::TaskCreated, serde_json::json!({})).with_actor(Actor::Go),
        )
        .unwrap();
        let events = log.read_all().unwrap();
        assert_eq!(events[0].actor, Actor::Go);
        drop(log);
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn concurrent_appends_through_shared_handle_stay_correctly_chained() {
        let dir = tmp("concurrent");
        let log = std::sync::Arc::new(AuditLog::open(&dir, "s").unwrap());
        let threads = 8;
        let per_thread = 25;
        let mut handles = Vec::new();
        for t in 0..threads {
            let log = log.clone();
            handles.push(std::thread::spawn(move || {
                let mut cursors = Vec::with_capacity(per_thread);
                for i in 0..per_thread {
                    let cursor = log
                        .append_with_cursor(&Event::new(
                            "s",
                            EventType::FileRead,
                            serde_json::json!({"thread": t, "i": i}),
                        ))
                        .unwrap();
                    cursors.push(cursor);
                }
                cursors
            }));
        }
        let mut all_cursors = Vec::new();
        for h in handles {
            all_cursors.extend(h.join().unwrap());
        }

        all_cursors.sort_unstable();
        let expected: Vec<usize> = (1..=threads * per_thread).collect();
        assert_eq!(
            all_cursors, expected,
            "cursor values must be exactly 1..=N with no gaps or duplicates"
        );

        let report = log.verify().unwrap();
        assert!(report.ok, "{:?}", report.reason);
        assert_eq!(report.event_count, threads * per_thread);
        drop(log);
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn persist_failure_poisoning_rejects_further_appends() {
        let dir = tmp("poisoned");
        let log = AuditLog::open(&dir, "s").unwrap();
        log.state.lock().unwrap().fail_next_persist = true;

        let first_error = log
            .append(&Event::new(
                "s",
                EventType::TaskCreated,
                serde_json::json!({}),
            ))
            .unwrap_err();
        assert!(matches!(first_error, AuditError::Io(_)));

        let second_error = log
            .append(&Event::new(
                "s",
                EventType::TaskCreated,
                serde_json::json!({}),
            ))
            .unwrap_err();
        assert!(matches!(second_error, AuditError::Poisoned));
        assert!(log.read_all().unwrap().is_empty());
        drop(log);
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
