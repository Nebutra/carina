//! The Pi-OS Capability Kernel facade (PRD §8.3).
//!
//! Invariant: **agents never touch system resources directly**. The control
//! plane routes every side effect through [`Kernel::request`], which
//! evaluates policy, records the decision in the audit log, and only then
//! lets the caller proceed.

use pi_audit::{AuditError, AuditLog, Event, EventType};
use pi_policy::{Capability, CapabilityRequest, Decision, PolicyEngine, Profile, Verdict};
use std::path::{Path, PathBuf};

pub use pi_audit;
pub use pi_policy;

#[derive(Debug, thiserror::Error)]
pub enum KernelError {
    #[error("unknown permission profile: {0}")]
    UnknownProfile(String),
    #[error(transparent)]
    Audit(#[from] AuditError),
}

/// A session-scoped kernel instance.
pub struct Kernel {
    session_id: String,
    workspace_root: PathBuf,
    profile: Profile,
    audit: AuditLog,
}

impl Kernel {
    pub fn new(
        session_id: impl Into<String>,
        workspace_root: impl Into<PathBuf>,
        profile_name: &str,
        audit_dir: &Path,
    ) -> Result<Self, KernelError> {
        let session_id = session_id.into();
        let profile = Profile::builtin(profile_name)
            .ok_or_else(|| KernelError::UnknownProfile(profile_name.to_string()))?;
        let audit = AuditLog::open(audit_dir, &session_id)?;
        Ok(Self {
            session_id,
            workspace_root: workspace_root.into(),
            profile,
            audit,
        })
    }

    /// Evaluates a capability request and records the outcome.
    ///
    /// Denials are additionally logged as `PolicyViolation` so audit reports
    /// can answer "why was this blocked" (PRD §8.2 acceptance criteria).
    pub fn request(&self, req: CapabilityRequest) -> Result<Decision, KernelError> {
        let decision = PolicyEngine::evaluate(&self.profile, &self.workspace_root, &req);

        let event_type = match decision.decision {
            Verdict::Allowed => EventType::ToolApproved,
            Verdict::RequiresApproval => EventType::ToolRequested,
            Verdict::Denied => EventType::PolicyViolation,
        };
        let payload = serde_json::json!({
            "capability": decision.capability,
            "resource": decision.resource,
            "decision": decision.decision,
            "reason": decision.reason,
            "policy_id": decision.policy_id,
        });
        let mut event = Event::new(&self.session_id, event_type, payload)
            .with_decision(&decision.decision_id);
        if let Some(task_id) = &req.task_id {
            event = event.with_task(task_id);
        }
        self.audit.append(&event)?;
        Ok(decision)
    }

    /// Convenience wrapper for the hot path: workspace file reads.
    pub fn request_file_read(&self, path: &str, task_id: Option<String>) -> Result<Decision, KernelError> {
        self.request(CapabilityRequest {
            capability: Capability::FileRead,
            requested_by: pi_policy::Principal::Agent,
            resource: path.to_string(),
            session_id: self.session_id.clone(),
            task_id,
        })
    }

    pub fn profile(&self) -> &Profile {
        &self.profile
    }

    pub fn audit(&self) -> &AuditLog {
        &self.audit
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn denied_request_leaves_policy_violation_in_audit_log() {
        let dir = std::env::temp_dir().join(format!("pi-kernel-test-{}", std::process::id()));
        let kernel = Kernel::new("sess_k", "/tmp/ws", "safe-edit", &dir).unwrap();

        let decision = kernel.request_file_read("/etc/passwd", None).unwrap();
        assert_eq!(decision.decision, Verdict::Denied);

        let events = kernel.audit().read_all().unwrap();
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].event_type, EventType::PolicyViolation);
        assert_eq!(
            events[0].permission_decision_id.as_deref(),
            Some(decision.decision_id.as_str())
        );
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn allowed_request_is_audited_as_approved() {
        let dir = std::env::temp_dir().join(format!("pi-kernel-test2-{}", std::process::id()));
        let ws = std::env::temp_dir();
        let kernel = Kernel::new("sess_k2", &ws, "safe-edit", &dir).unwrap();

        let inside = ws.join("main.rs");
        let decision = kernel.request_file_read(inside.to_str().unwrap(), None).unwrap();
        assert_eq!(decision.decision, Verdict::Allowed);

        let events = kernel.audit().read_all().unwrap();
        assert_eq!(events[0].event_type, EventType::ToolApproved);
        std::fs::remove_dir_all(&dir).ok();
    }
}
