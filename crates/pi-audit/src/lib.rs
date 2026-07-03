//! Append-only event log for Pi-OS (PRD §8.2).
//!
//! One JSONL file per session; events mirror
//! `protocol/schemas/event.schema.json` and the enumeration in
//! `protocol/events/events.json`.

use serde::{Deserialize, Serialize};
use std::fs::OpenOptions;
use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use time::format_description::well_known::Rfc3339;
use time::OffsetDateTime;

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

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Event {
    pub event_id: String,
    pub session_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task_id: Option<String>,
    #[serde(rename = "type")]
    pub event_type: EventType,
    /// RFC 3339 timestamp.
    pub timestamp: String,
    #[serde(default)]
    pub payload: serde_json::Value,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub permission_decision_id: Option<String>,
}

impl Event {
    pub fn new(session_id: impl Into<String>, event_type: EventType, payload: serde_json::Value) -> Self {
        Self {
            event_id: new_event_id(),
            session_id: session_id.into(),
            task_id: None,
            event_type,
            timestamp: OffsetDateTime::now_utc()
                .format(&Rfc3339)
                .unwrap_or_else(|_| "1970-01-01T00:00:00Z".into()),
            payload,
            permission_decision_id: None,
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
}

#[derive(Debug, thiserror::Error)]
pub enum AuditError {
    #[error("audit io: {0}")]
    Io(#[from] std::io::Error),
    #[error("audit serialization: {0}")]
    Serde(#[from] serde_json::Error),
}

/// Append-only JSONL audit log for a single session.
pub struct AuditLog {
    path: PathBuf,
}

impl AuditLog {
    /// Opens (or creates) the log for `session_id` under `dir`.
    pub fn open(dir: &Path, session_id: &str) -> Result<Self, AuditError> {
        std::fs::create_dir_all(dir)?;
        Ok(Self {
            path: dir.join(format!("{session_id}.events.jsonl")),
        })
    }

    /// Appends one event. The log is never rewritten or truncated.
    pub fn append(&self, event: &Event) -> Result<(), AuditError> {
        let mut file = OpenOptions::new().create(true).append(true).open(&self.path)?;
        let line = serde_json::to_string(event)?;
        file.write_all(line.as_bytes())?;
        file.write_all(b"\n")?;
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

    pub fn path(&self) -> &Path {
        &self.path
    }
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

    #[test]
    fn append_then_replay_roundtrip() {
        let dir = std::env::temp_dir().join(format!("pi-audit-test-{}", std::process::id()));
        let log = AuditLog::open(&dir, "sess_test").unwrap();
        let ev = Event::new("sess_test", EventType::TaskCreated, serde_json::json!({"task_id": "task_1"}));
        log.append(&ev).unwrap();
        let events = log.read_all().unwrap();
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].event_type, EventType::TaskCreated);
        std::fs::remove_dir_all(&dir).ok();
    }
}
