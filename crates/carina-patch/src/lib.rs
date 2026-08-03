//! Transactional patch engine (PRD §8.4).
//!
//! Lifecycle: `Proposed → Validated → Approved → Applied → Verified →
//! Committed`, with `RolledBack` reachable from any post-apply state and
//! `Failed` from any pre-commit state. Illegal transitions are rejected —
//! a patch can never end up half-applied.
//!
//! Phase 0 models the state machine, hashing, and conflict detection.
//! Atomic filesystem apply is delegated to `zig/carina-patch-native` in Phase 1.

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

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct VerifyResult {
    pub status: TestStatus,
    pub expected_hash: String,
    pub actual_hash: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HunkAttribution {
    pub file: String,
    pub hunk_index: usize,
    pub actor: String,
    pub transaction_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub approval_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub audit_event_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub verify_result: Option<TestStatus>,
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
    /// Stable transaction identity. Equal to patch_id for the current MVP.
    pub transaction_id: String,
    pub session_id: String,
    pub actor: String,
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
    #[serde(skip_serializing_if = "Option::is_none")]
    pub approval_id: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub audit_event_ids: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub verify_result: Option<VerifyResult>,
    pub hunk_attributions: Vec<HunkAttribution>,
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
        let patch_id = new_patch_id();
        let hunk_attributions = affected_files
            .iter()
            .enumerate()
            .map(|(hunk_index, file)| HunkAttribution {
                file: file.clone(),
                hunk_index,
                actor: "agent".into(),
                transaction_id: patch_id.clone(),
                approval_id: None,
                audit_event_id: None,
                verify_result: None,
            })
            .collect();
        Ok(Self {
            patch_id: patch_id.clone(),
            transaction_id: patch_id,
            session_id: session_id.into(),
            actor: "agent".into(),
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
            approval_id: None,
            audit_event_ids: Vec::new(),
            verify_result: None,
            hunk_attributions,
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
        self.approval_status = if auto {
            ApprovalStatus::Auto
        } else {
            ApprovalStatus::Approved
        };
        self.transition(PatchStatus::Approved)
    }

    pub fn with_approval(mut self, approval_id: impl Into<String>) -> Self {
        let approval_id = approval_id.into();
        self.approval_id = Some(approval_id.clone());
        for hunk in &mut self.hunk_attributions {
            hunk.approval_id = Some(approval_id.clone());
        }
        self
    }

    pub fn with_audit_event(mut self, event_id: impl Into<String>) -> Self {
        let event_id = event_id.into();
        self.audit_event_ids.push(event_id.clone());
        for hunk in &mut self.hunk_attributions {
            hunk.audit_event_id = Some(event_id.clone());
        }
        self
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
        self.test_status = if tests_passed {
            TestStatus::Passed
        } else {
            TestStatus::Failed
        };
        if tests_passed {
            self.transition(PatchStatus::Verified)
        } else {
            self.transition(PatchStatus::Failed)
        }
    }

    pub fn with_verify_result(mut self, expected_hash: String, actual_hash: String) -> Self {
        self.verify_result = Some(VerifyResult {
            status: self.test_status,
            expected_hash,
            actual_hash,
        });
        for hunk in &mut self.hunk_attributions {
            hunk.verify_result = Some(self.test_status);
        }
        self
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
            return Err(PatchError::IllegalTransition {
                from: self.status,
                to,
            });
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
        PatchTransaction::propose(
            "sess_1",
            vec!["src/auth.ts".into()],
            b"old",
            "-old\n+new",
            "fix auth",
        )
        .unwrap()
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

    #[test]
    fn empty_patch_rejected() {
        let err = PatchTransaction::propose("s", vec![], b"", "d", "r").unwrap_err();
        assert!(matches!(err, PatchError::Empty));
    }

    #[test]
    fn fail_from_various_states() {
        assert_eq!(proposed().fail().unwrap().status, PatchStatus::Failed);
        let validated = proposed().validate(b"old").unwrap();
        assert_eq!(validated.fail().unwrap().status, PatchStatus::Failed);
    }

    #[test]
    fn verify_failure_transitions_to_failed() {
        let p = proposed()
            .validate(b"old")
            .unwrap()
            .approve(true)
            .unwrap()
            .mark_applied(b"new", "snap")
            .unwrap()
            .mark_verified(false)
            .unwrap();
        assert_eq!(p.status, PatchStatus::Failed);
        assert_eq!(p.test_status, TestStatus::Failed);
    }

    #[test]
    fn rollback_from_committed_ok() {
        let committed = proposed()
            .validate(b"old")
            .unwrap()
            .approve(false)
            .unwrap()
            .mark_applied(b"new", "snap")
            .unwrap()
            .mark_verified(true)
            .unwrap()
            .commit()
            .unwrap();
        assert_eq!(
            committed.clone().rollback().unwrap().status,
            PatchStatus::RolledBack
        );
    }

    #[test]
    fn provenance_and_auto_approval() {
        let p = proposed().with_provenance(
            Some("task_1".into()),
            Some("step_2".into()),
            Some("claude".into()),
        );
        assert_eq!(p.task_id.as_deref(), Some("task_1"));
        assert_eq!(p.agent_step_id.as_deref(), Some("step_2"));
        assert_eq!(p.model_id.as_deref(), Some("claude"));
        assert!(!p.created_at.is_empty());
        // auto approval marks ApprovalStatus::Auto
        let approved = p.validate(b"old").unwrap().approve(true).unwrap();
        assert_eq!(approved.approval_status, ApprovalStatus::Auto);
    }

    #[test]
    fn content_hash_is_stable() {
        assert_eq!(content_hash(b"abc"), content_hash(b"abc"));
        assert_ne!(content_hash(b"abc"), content_hash(b"abd"));
    }
}
