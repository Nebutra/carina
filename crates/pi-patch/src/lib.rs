//! Transactional patch engine (PRD §8.4).
//!
//! Lifecycle: `Proposed → Validated → Approved → Applied → Verified →
//! Committed`, with `RolledBack` reachable from any post-apply state and
//! `Failed` from any pre-commit state. Illegal transitions are rejected —
//! a patch can never end up half-applied.
//!
//! Phase 0 models the state machine, hashing, and conflict detection.
//! Atomic filesystem apply is delegated to `zig/pi-patch-native` in Phase 1.

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PatchStatus {
    Proposed,
    Validated,
    Approved,
    Applied,
    Verified,
    Committed,
    RolledBack,
    Failed,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ApprovalStatus {
    Pending,
    Approved,
    Denied,
    Auto,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TestStatus {
    NotRun,
    Passed,
    Failed,
}

#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum PatchError {
    #[error("illegal transition {from:?} -> {to:?}")]
    IllegalTransition { from: PatchStatus, to: PatchStatus },
    #[error("base hash mismatch for {file}: expected {expected}, found {found}")]
    Conflict {
        file: String,
        expected: String,
        found: String,
    },
    #[error("patch affects no files")]
    Empty,
}

/// Mirrors protocol/schemas/patch-transaction.schema.json.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PatchTransaction {
    pub patch_id: String,
    pub session_id: String,
    /// Provenance: the task, agent step, and model that produced the patch.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub agent_step_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub model_id: Option<String>,
    /// When the patch was proposed (unix epoch milliseconds as a string).
    pub created_at: String,
    pub status: PatchStatus,
    pub affected_files: Vec<String>,
    /// Hash of the pre-image the diff was computed against.
    pub base_hash: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub new_hash: Option<String>,
    pub diff: String,
    pub reason: String,
    pub risk_level: u8,
    pub approval_status: ApprovalStatus,
    pub test_status: TestStatus,
    /// Snapshot id used to restore the pre-image on rollback.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub rollback_pointer: Option<String>,
}

impl PatchTransaction {
    pub fn propose(
        session_id: impl Into<String>,
        affected_files: Vec<String>,
        base_content: &[u8],
        diff: impl Into<String>,
        reason: impl Into<String>,
    ) -> Result<Self, PatchError> {
        if affected_files.is_empty() {
            return Err(PatchError::Empty);
        }
        Ok(Self {
            patch_id: new_patch_id(),
            session_id: session_id.into(),
            task_id: None,
            agent_step_id: None,
            model_id: None,
            created_at: now_millis(),
            status: PatchStatus::Proposed,
            affected_files,
            base_hash: content_hash(base_content),
            new_hash: None,
            diff: diff.into(),
            reason: reason.into(),
            risk_level: 2,
            approval_status: ApprovalStatus::Pending,
            test_status: TestStatus::NotRun,
            rollback_pointer: None,
        })
    }

    /// Attaches provenance: which task/agent-step/model produced this patch
    /// (PRD §7.6).
    pub fn with_provenance(
        mut self,
        task_id: Option<String>,
        agent_step_id: Option<String>,
        model_id: Option<String>,
    ) -> Self {
        self.task_id = task_id;
        self.agent_step_id = agent_step_id;
        self.model_id = model_id;
        self
    }

    /// Pre-apply validation: the current on-disk content must still match
    /// the pre-image this patch was computed against (conflict detection).
    pub fn validate(self, current_content: &[u8]) -> Result<Self, PatchError> {
        let found = content_hash(current_content);
        if found != self.base_hash {
            return Err(PatchError::Conflict {
                file: self.affected_files.join(","),
                expected: self.base_hash.clone(),
                found,
            });
        }
        self.transition(PatchStatus::Validated)
    }

    pub fn approve(mut self, auto: bool) -> Result<Self, PatchError> {
        self.approval_status = if auto { ApprovalStatus::Auto } else { ApprovalStatus::Approved };
        self.transition(PatchStatus::Approved)
    }

    /// Records a successful atomic apply with its rollback snapshot.
    pub fn mark_applied(
        mut self,
        new_content: &[u8],
        rollback_pointer: impl Into<String>,
    ) -> Result<Self, PatchError> {
        self.new_hash = Some(content_hash(new_content));
        self.rollback_pointer = Some(rollback_pointer.into());
        self.transition(PatchStatus::Applied)
    }

    pub fn mark_verified(mut self, tests_passed: bool) -> Result<Self, PatchError> {
        self.test_status = if tests_passed { TestStatus::Passed } else { TestStatus::Failed };
        if tests_passed {
            self.transition(PatchStatus::Verified)
        } else {
            self.transition(PatchStatus::Failed)
        }
    }

    pub fn commit(self) -> Result<Self, PatchError> {
        self.transition(PatchStatus::Committed)
    }

    pub fn rollback(self) -> Result<Self, PatchError> {
        self.transition(PatchStatus::RolledBack)
    }

    pub fn fail(self) -> Result<Self, PatchError> {
        self.transition(PatchStatus::Failed)
    }

    fn transition(mut self, to: PatchStatus) -> Result<Self, PatchError> {
        use PatchStatus::*;
        let legal = matches!(
            (self.status, to),
            (Proposed, Validated)
                | (Validated, Approved)
                | (Approved, Applied)
                | (Applied, Verified)
                | (Verified, Committed)
                | (Applied, RolledBack)
                | (Verified, RolledBack)
                | (Committed, RolledBack)
                | (Proposed, Failed)
                | (Validated, Failed)
                | (Approved, Failed)
                | (Applied, Failed)
                | (Verified, Failed)
        );
        if !legal {
            return Err(PatchError::IllegalTransition { from: self.status, to });
        }
        self.status = to;
        Ok(self)
    }
}

/// SHA-256 content hash used for provenance and conflict detection.
pub fn content_hash(content: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(content);
    format!("{:x}", hasher.finalize())
}

fn new_patch_id() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or_default();
    format!("patch_{nanos:x}")
}

fn now_millis() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let millis = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or_default();
    millis.to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn proposed() -> PatchTransaction {
        PatchTransaction::propose("sess_1", vec!["src/auth.ts".into()], b"old", "-old\n+new", "fix auth").unwrap()
    }

    #[test]
    fn happy_path_reaches_committed() {
        let p = proposed()
            .validate(b"old")
            .unwrap()
            .approve(false)
            .unwrap()
            .mark_applied(b"new", "snapshot_1")
            .unwrap()
            .mark_verified(true)
            .unwrap()
            .commit()
            .unwrap();
        assert_eq!(p.status, PatchStatus::Committed);
        assert!(p.rollback_pointer.is_some());
    }

    #[test]
    fn concurrent_modification_is_a_conflict() {
        let err = proposed().validate(b"changed by someone else").unwrap_err();
        assert!(matches!(err, PatchError::Conflict { .. }));
    }

    #[test]
    fn cannot_apply_without_approval() {
        let err = proposed().mark_applied(b"new", "snap").unwrap_err();
        assert!(matches!(err, PatchError::IllegalTransition { .. }));
    }

    #[test]
    fn rollback_only_after_apply() {
        assert!(proposed().rollback().is_err());
    }
}
