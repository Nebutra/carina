//! The Carina Capability Kernel facade (PRD §8.3).
//!
//! Invariant: **agents never touch system resources directly**. The control
//! plane routes every side effect through [`Kernel::request`], which
//! evaluates policy, records the decision in the audit log, and only then
//! lets the caller proceed.

mod secret;
pub use secret::SecretBroker;

use carina_audit::{AuditError, AuditLog, Event, EventType};
use carina_policy::{ApprovalMode, Capability, CapabilityRequest, Decision, PolicyBundle, PolicyEngine, Profile, Verdict};
use std::cell::RefCell;
use std::collections::HashMap;
use std::path::{Path, PathBuf};

pub use carina_audit;
pub use carina_policy;

#[derive(Debug, thiserror::Error)]
pub enum KernelError {
    #[error("unknown permission profile: {0}")]
    UnknownProfile(String),
    #[error("plugin error: {0}")]
    Plugin(String),
    #[error(transparent)]
    Audit(#[from] AuditError),
}

/// Bridges a plugin's capability request to the session policy engine. The
/// plugin runtime has already checked the manifest; this is the second gate
/// (PRD §8.7: plugins are limited to their authorized scope), and it also
/// applies the org policy bundle.
struct ProfileHost {
    profile: Profile,
    bundle: Option<PolicyBundle>,
    workspace_root: PathBuf,
}

impl carina_plugin_runtime::CapabilityHost for ProfileHost {
    fn allow(&self, capability: &str, resource: &str) -> bool {
        let cap = match capability {
            "file_read" => Capability::FileRead,
            "file_write" => Capability::FileWrite,
            "command_exec" => Capability::CommandExec,
            "network" => Capability::NetworkAccess,
            "secret" => Capability::SecretRead,
            "memory_write" => Capability::MemoryWrite,
            _ => return false,
        };
        let request = CapabilityRequest {
            capability: cap,
            requested_by: carina_policy::Principal::Plugin,
            resource: resource.to_string(),
            session_id: String::new(),
            task_id: None,
        };
        PolicyEngine::evaluate_with_bundle(&self.profile, self.bundle.as_ref(), &self.workspace_root, &request)
            .decision
            == Verdict::Allowed
    }
}

/// A session-scoped kernel instance.
pub struct Kernel {
    session_id: String,
    workspace_root: PathBuf,
    /// Extra roots granted at runtime (the `/add-dir` scoped grant). A path
    /// capability is evaluated against whichever root contains it, so a session
    /// can be widened to additional trees without loosening the profile.
    additional_roots: Vec<PathBuf>,
    profile: Profile,
    bundle: Option<PolicyBundle>,
    approval_mode: ApprovalMode,
    /// Approval overlays (Codex ApprovedForSession): capability|resource-prefix
    /// rules approved for the whole session, with an auditable rationale.
    approval_overlays: RefCell<HashMap<String, ApprovalOverlay>>,
    audit: AuditLog,
    secrets: SecretBroker,
    verifier: carina_plugin_runtime::SignatureVerifier,
    approval_policy: ApprovalPolicy,
}

/// Role-based approval policy (PRD §5 Phase 5). Maps a minimum command risk
/// level to the role required to approve it. An approver lacking the role is
/// rejected.
#[derive(Debug, Clone, Default)]
pub struct ApprovalPolicy {
    /// risk level (inclusive) -> required role
    pub required_role_at_risk: Vec<(u8, String)>,
}

#[derive(Debug, Clone)]
struct ApprovalOverlay {
    overlay_id: String,
    capability: Capability,
    resource_prefix: String,
    source_decision_id: String,
    approver: String,
    justification: String,
    created_at_ms: u128,
}

impl ApprovalPolicy {
    /// Returns the role required to approve a command of the given risk, if any.
    pub fn required_role(&self, risk: u8) -> Option<&str> {
        self.required_role_at_risk
            .iter()
            .filter(|(threshold, _)| risk >= *threshold)
            .max_by_key(|(threshold, _)| *threshold)
            .map(|(_, role)| role.as_str())
    }
}

impl Kernel {
    pub fn new(
        session_id: impl Into<String>,
        workspace_root: impl Into<PathBuf>,
        profile_name: &str,
        audit_dir: &Path,
    ) -> Result<Self, KernelError> {
        let profile = Profile::builtin(profile_name)
            .ok_or_else(|| KernelError::UnknownProfile(profile_name.to_string()))?;
        Self::with_profile(session_id, workspace_root, profile, audit_dir)
    }

    /// Builds a kernel from an explicit profile (e.g. a custom profile
    /// loaded from TOML). This is how custom permission profiles enter the
    /// runtime (PRD §8.3).
    pub fn with_profile(
        session_id: impl Into<String>,
        workspace_root: impl Into<PathBuf>,
        profile: Profile,
        audit_dir: &Path,
    ) -> Result<Self, KernelError> {
        let session_id = session_id.into();
        let audit = AuditLog::open(audit_dir, &session_id)?;
        Ok(Self {
            session_id,
            workspace_root: workspace_root.into(),
            additional_roots: Vec::new(),
            profile,
            bundle: None,
            approval_mode: ApprovalMode::default(),
            approval_overlays: RefCell::new(HashMap::new()),
            audit,
            secrets: SecretBroker::new(),
            verifier: carina_plugin_runtime::SignatureVerifier::new(),
            approval_policy: ApprovalPolicy::default(),
        })
    }

    /// Sets the approval mode (the "when to ask" axis, orthogonal to the
    /// profile's "what can you do" axis).
    pub fn set_approval_mode(&mut self, mode: ApprovalMode) {
        self.approval_mode = mode;
    }

    fn resource_prefix(resource: &str) -> String {
        let prefix = resource.split_whitespace().next().unwrap_or(resource);
        prefix.to_string()
    }

    fn cache_key(cap: Capability, resource: &str) -> String {
        // Cache by capability + a coarse resource prefix (first path/word).
        let prefix = Self::resource_prefix(resource);
        format!("{cap:?}|{prefix}")
    }

    /// Attaches an organization policy bundle that can only tighten this
    /// session's profile (PRD §5 Phase 5: team policy / policy bundle).
    pub fn set_bundle(&mut self, bundle: PolicyBundle) {
        self.bundle = Some(bundle);
    }

    /// Trusts an ed25519 publisher key for signed-plugin verification.
    pub fn trust_plugin_key(&mut self, key_bytes: &[u8]) -> Result<(), KernelError> {
        self.verifier.trust_key(key_bytes).map_err(|e| KernelError::Plugin(e.to_string()))
    }

    /// Sets the role-based approval policy.
    pub fn set_approval_policy(&mut self, policy: ApprovalPolicy) {
        self.approval_policy = policy;
    }

    /// Grants the session an additional allowed root (`/add-dir`). Paths within
    /// it are thereafter evaluated as in-workspace. Idempotent.
    pub fn add_dir(&mut self, path: impl Into<PathBuf>) {
        let path = path.into();
        if !self.additional_roots.contains(&path) {
            self.additional_roots.push(path);
        }
    }

    /// The additional roots granted to this session.
    pub fn additional_roots(&self) -> &[PathBuf] {
        &self.additional_roots
    }

    /// Selects the root a path resource is evaluated against: an additional root
    /// that contains it, else the primary workspace root. For non-path
    /// capabilities the choice is irrelevant (the policy engine ignores the root).
    fn effective_root(&self, resource: &str) -> &Path {
        let candidate = Path::new(resource);
        for root in &self.additional_roots {
            if carina_policy::path_within_workspace(root, candidate) {
                return root;
            }
        }
        &self.workspace_root
    }

    /// Mutable access to the session's secret broker.
    pub fn secrets_mut(&mut self) -> &mut SecretBroker {
        &mut self.secrets
    }

    pub fn secrets(&self) -> &SecretBroker {
        &self.secrets
    }

    /// Requests a secret by name. Returns the opaque handle only if policy
    /// allows and the secret is registered; the plaintext never leaves the
    /// kernel and only `secret_handle` is written to the audit log.
    pub fn request_secret(&self, name: &str) -> Result<(Decision, Option<String>), KernelError> {
        let request = CapabilityRequest {
            capability: Capability::SecretRead,
            requested_by: carina_policy::Principal::Agent,
            resource: name.to_string(),
            session_id: self.session_id.clone(),
            task_id: None,
        };
        let decision = PolicyEngine::evaluate(&self.profile, &self.workspace_root, &request);
        let handle = if decision.decision == Verdict::Allowed {
            self.secrets.handle(name)
        } else {
            None
        };
        // Audit event records the handle, never the value.
        let payload = serde_json::json!({
            "secret_handle": handle,
            "decision": decision.decision,
            "reason": decision.reason,
        });
        let event = Event::new(&self.session_id, EventType::SecretRequested, payload)
            .with_decision(&decision.decision_id);
        self.audit.append(&event)?;
        Ok((decision, handle))
    }

    /// Evaluates a capability request and records the outcome.
    ///
    /// Denials are additionally logged as `PolicyViolation` so audit reports
    /// can answer "why was this blocked" (PRD §8.2 acceptance criteria).
    pub fn request(&self, req: CapabilityRequest) -> Result<Decision, KernelError> {
        let root = self.effective_root(&req.resource);
        let mut decision =
            PolicyEngine::evaluate_with_bundle(&self.profile, self.bundle.as_ref(), root, &req);
        // Apply the orthogonal approval-mode axis (Codex two-axis model).
        decision = carina_policy::apply_approval_mode(self.approval_mode, decision);
        // Approval overlay: a prior ApprovedForSession auto-satisfies only
        // after policy reached requires_approval. Denials are never rescued.
        let mut overlay_hit: Option<ApprovalOverlay> = None;
        if decision.decision == Verdict::RequiresApproval {
            let key = Self::cache_key(decision.capability, &decision.resource);
            if let Some(overlay) = self.approval_overlays.borrow().get(&key).cloned() {
                decision.decision = Verdict::Allowed;
                decision.reason = format!(
                    "approved by session overlay {}: {}",
                    overlay.overlay_id, overlay.justification
                );
                overlay_hit = Some(overlay);
            }
        }

        let event_type = match decision.decision {
            Verdict::Allowed => EventType::ToolApproved,
            Verdict::RequiresApproval => EventType::ToolRequested,
            Verdict::Denied => EventType::PolicyViolation,
        };
        let mut payload = serde_json::json!({
            "capability": decision.capability,
            "resource": decision.resource,
            "decision": decision.decision,
            "reason": decision.reason,
            "policy_id": decision.policy_id,
        });
        if let Some(overlay) = overlay_hit {
            payload["approval_overlay"] = serde_json::json!({
                "overlay_id": overlay.overlay_id,
                "capability": overlay.capability,
                "resource_prefix": overlay.resource_prefix,
                "source_decision_id": overlay.source_decision_id,
                "approver": overlay.approver,
                "justification": overlay.justification,
                "created_at_ms": overlay.created_at_ms.to_string(),
            });
        }
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
            requested_by: carina_policy::Principal::Agent,
            resource: path.to_string(),
            session_id: self.session_id.clone(),
            task_id,
        })
    }

    /// Resolves a `requires_approval` decision. The approval itself is an
    /// audit event (`ToolApproved`) that records who approved it.
    pub fn approve(&self, decision: &Decision, approver: &str) -> Result<Decision, KernelError> {
        self.approve_as(decision, approver, None)
    }

    /// Approves and remembers for the whole session: subsequent requests for
    /// the same capability+resource-prefix auto-satisfy (Codex
    /// ApprovedForSession — cuts approval fatigue on long tasks).
    pub fn approve_for_session(&self, decision: &Decision, approver: &str) -> Result<Decision, KernelError> {
        self.approve_for_session_with_justification(decision, approver, "approved for session")
    }

    /// Approves and remembers for the whole session with an explicit
    /// justification. The overlay can only satisfy future requires_approval
    /// decisions; denied requests remain denied.
    pub fn approve_for_session_with_justification(
        &self,
        decision: &Decision,
        approver: &str,
        justification: &str,
    ) -> Result<Decision, KernelError> {
        let key = Self::cache_key(decision.capability, &decision.resource);
        let overlay = ApprovalOverlay {
            overlay_id: new_overlay_id(),
            capability: decision.capability,
            resource_prefix: Self::resource_prefix(&decision.resource),
            source_decision_id: decision.decision_id.clone(),
            approver: approver.to_string(),
            justification: if justification.trim().is_empty() {
                "approved for session".into()
            } else {
                justification.trim().to_string()
            },
            created_at_ms: now_ms(),
        };
        self.approval_overlays.borrow_mut().insert(key, overlay.clone());
        let approved = self.approve_as(decision, approver, None)?;
        let event = Event::new(
            &self.session_id,
            EventType::ToolApproved,
            serde_json::json!({
                "status": "approval_overlay_created",
                "overlay_id": overlay.overlay_id,
                "capability": overlay.capability,
                "resource_prefix": overlay.resource_prefix,
                "source_decision_id": overlay.source_decision_id,
                "approver": overlay.approver,
                "justification": overlay.justification,
                "created_at_ms": overlay.created_at_ms.to_string(),
            }),
        )
        .with_decision(&decision.decision_id);
        self.audit.append(&event)?;
        Ok(approved)
    }

    /// Role-aware approval (PRD §5 Phase 5). If the approval policy requires
    /// a role for this action's risk level and `role` doesn't match, the
    /// approval is rejected and recorded as denied.
    pub fn approve_as(
        &self,
        decision: &Decision,
        approver: &str,
        role: Option<&str>,
    ) -> Result<Decision, KernelError> {
        if decision.capability == Capability::CommandExec {
            let risk = carina_policy::classify_command(&decision.resource);
            if let Some(required) = self.approval_policy.required_role(risk) {
                if role != Some(required) {
                    let reason = format!(
                        "approval rejected: risk {risk} requires role '{required}', approver had {:?}",
                        role
                    );
                    let denied = Decision { decision: Verdict::Denied, reason: reason.clone(), ..decision.clone() };
                    let event = Event::new(
                        &self.session_id,
                        EventType::ToolDenied,
                        serde_json::json!({"approver": approver, "role": role, "required_role": required, "reason": reason}),
                    )
                    .with_decision(&denied.decision_id);
                    self.audit.append(&event)?;
                    return Ok(denied);
                }
            }
        }
        let approved = Decision {
            decision: Verdict::Allowed,
            reason: format!("approved by {approver} ({})", decision.reason),
            ..decision.clone()
        };
        let payload = serde_json::json!({
            "capability": approved.capability,
            "resource": approved.resource,
            "approver": approver,
        });
        let event = Event::new(&self.session_id, EventType::ToolApproved, payload)
            .with_decision(&approved.decision_id);
        self.audit.append(&event)?;
        Ok(approved)
    }

    /// Records a denial issued by a human reviewer.
    pub fn deny(&self, decision: &Decision, approver: &str, reason: &str) -> Result<Decision, KernelError> {
        let denied = Decision {
            decision: Verdict::Denied,
            reason: format!("denied by {approver}: {reason}"),
            ..decision.clone()
        };
        let payload = serde_json::json!({
            "capability": denied.capability,
            "resource": denied.resource,
            "approver": approver,
            "reason": reason,
        });
        let event = Event::new(&self.session_id, EventType::ToolDenied, payload)
            .with_decision(&denied.decision_id);
        self.audit.append(&event)?;
        Ok(denied)
    }

    /// Appends an arbitrary event to this session's audit log (used by the
    /// control plane for lifecycle events like CommandStarted).
    pub fn record_event(&self, event: &Event) -> Result<(), KernelError> {
        self.audit.append(event)?;
        Ok(())
    }

    /// Records an event and returns the exclusive raw audit cursor without
    /// replaying the JSONL log.
    pub fn record_event_with_cursor(&self, event: &Event) -> Result<usize, KernelError> {
        Ok(self.audit.append_with_cursor(event)?)
    }

    /// Runs a WASM plugin under this session's policy (PRD §8.7). Every
    /// capability the plugin requests is gated by both its manifest and the
    /// session profile, and each decision is written to the audit log.
    pub fn run_plugin(
        &self,
        manifest: &carina_plugin_runtime::Manifest,
        wasm: &[u8],
    ) -> Result<carina_plugin_runtime::RunOutcome, KernelError> {
        self.run_plugin_signed(manifest, wasm, None)
    }

    /// Runs a plugin, optionally verifying an ed25519 signature over the
    /// module bytes when the deployment trusts publisher keys.
    pub fn run_plugin_signed(
        &self,
        manifest: &carina_plugin_runtime::Manifest,
        wasm: &[u8],
        signature: Option<&[u8]>,
    ) -> Result<carina_plugin_runtime::RunOutcome, KernelError> {
        // Record that the plugin was loaded (PluginLoad capability).
        let load_event = Event::new(
            &self.session_id,
            EventType::ToolRequested,
            serde_json::json!({"plugin": manifest.name, "version": manifest.version}),
        );
        self.audit.append(&load_event)?;

        // If any publisher keys are trusted, the plugin MUST be signed by one
        // of them (PRD §5: signed plugin). Unsigned/untrusted → refused.
        if !self.verifier.is_empty() {
            match signature {
                Some(sig) => self
                    .verifier
                    .verify(wasm, sig)
                    .map_err(|e| KernelError::Plugin(format!("signature check failed: {e}")))?,
                None => {
                    return Err(KernelError::Plugin(
                        "plugin is unsigned but this deployment requires signed plugins".into(),
                    ))
                }
            }
        }

        let host = ProfileHost {
            profile: self.profile.clone(),
            bundle: self.bundle.clone(),
            workspace_root: self.workspace_root.clone(),
        };
        let outcome = carina_plugin_runtime::PluginRuntime::new()
            .run(manifest, wasm, Box::new(host))
            .map_err(|e| KernelError::Plugin(e.to_string()))?;

        // Audit each capability decision the plugin made.
        for d in &outcome.decisions {
            let event_type = if d.allowed { EventType::ToolApproved } else { EventType::PolicyViolation };
            let event = Event::new(
                &self.session_id,
                event_type,
                serde_json::json!({
                    "plugin": manifest.name,
                    "capability": d.capability,
                    "resource": d.resource,
                    "reason": d.reason,
                }),
            );
            self.audit.append(&event)?;
        }
        Ok(outcome)
    }

    /// Exports the full audit bundle for centralized/enterprise audit
    /// (PRD §5 Phase 5: centralized audit). Returns the session id, event
    /// count, and every event in append order.
    pub fn export_audit(&self) -> Result<serde_json::Value, KernelError> {
        let events = self.audit.read_all()?;
        Ok(serde_json::json!({
            "session_id": self.session_id,
            "profile": self.profile.name,
            "event_count": events.len(),
            "events": events,
        }))
    }

    pub fn session_id(&self) -> &str {
        &self.session_id
    }

    pub fn workspace_root(&self) -> &Path {
        &self.workspace_root
    }

    pub fn profile(&self) -> &Profile {
        &self.profile
    }

    pub fn audit(&self) -> &AuditLog {
        &self.audit
    }
}

fn now_ms() -> u128 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis()
}

fn new_overlay_id() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
    format!("approval_overlay_{nanos:x}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn denied_request_leaves_policy_violation_in_audit_log() {
        let dir = std::env::temp_dir().join(format!("carina-kernel-test-{}", std::process::id()));
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
        let dir = std::env::temp_dir().join(format!("carina-kernel-test2-{}", std::process::id()));
        let ws = std::env::temp_dir();
        let kernel = Kernel::new("sess_k2", &ws, "safe-edit", &dir).unwrap();

        let inside = ws.join("main.rs");
        let decision = kernel.request_file_read(inside.to_str().unwrap(), None).unwrap();
        assert_eq!(decision.decision, Verdict::Allowed);

        let events = kernel.audit().read_all().unwrap();
        assert_eq!(events[0].event_type, EventType::ToolApproved);
        std::fs::remove_dir_all(&dir).ok();
    }

    fn tmp(name: &str) -> PathBuf {
        use std::time::{SystemTime, UNIX_EPOCH};
        let n = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
        std::env::temp_dir().join(format!("carina-kernel-{name}-{}-{n:x}", std::process::id()))
    }

    #[test]
    fn add_dir_grants_an_additional_root() {
        let audit = tmp("adddir-audit");
        let ws = tmp("adddir-ws");
        let extra = tmp("adddir-extra");
        std::fs::create_dir_all(&ws).unwrap();
        std::fs::create_dir_all(&extra).unwrap();

        let mut kernel = Kernel::new("sess_ad", &ws, "safe-edit", &audit).unwrap();
        let target = extra.join("notes.txt");

        // Before the grant: a read outside the workspace is denied.
        let before = kernel.request_file_read(target.to_str().unwrap(), None).unwrap();
        assert_eq!(before.decision, Verdict::Denied);

        // After /add-dir: the same read is allowed (evaluated against the added root).
        kernel.add_dir(&extra);
        let after = kernel.request_file_read(target.to_str().unwrap(), None).unwrap();
        assert_eq!(after.decision, Verdict::Allowed);

        // A path in neither root remains denied.
        let elsewhere = tmp("adddir-other").join("x.txt");
        let denied = kernel.request_file_read(elsewhere.to_str().unwrap(), None).unwrap();
        assert_eq!(denied.decision, Verdict::Denied);

        std::fs::remove_dir_all(&audit).ok();
        std::fs::remove_dir_all(&ws).ok();
        std::fs::remove_dir_all(&extra).ok();
    }

    #[test]
    fn unknown_profile_errors() {
        let dir = tmp("badprofile");
        assert!(Kernel::new("s", "/tmp/ws", "no-such-profile", &dir).is_err());
    }

    #[test]
    fn approve_and_deny_are_audited() {
        let dir = tmp("approve");
        let kernel = Kernel::new("sess_ap", "/tmp/ws", "safe-edit", &dir).unwrap();
        // Build a requires_approval decision (risk-2 command).
        let decision = kernel
            .request(CapabilityRequest {
                capability: Capability::CommandExec,
                requested_by: carina_policy::Principal::Agent,
                resource: "npm install x".into(),
                session_id: "sess_ap".into(),
                task_id: None,
            })
            .unwrap();
        assert_eq!(decision.decision, Verdict::RequiresApproval);

        let approved = kernel.approve(&decision, "alice").unwrap();
        assert_eq!(approved.decision, Verdict::Allowed);
        let denied = kernel.deny(&decision, "bob", "nope").unwrap();
        assert_eq!(denied.decision, Verdict::Denied);

        // approve + deny each appended an event beyond the original request.
        assert!(kernel.audit().read_all().unwrap().len() >= 3);
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn approval_overlay_records_justification_and_auto_satisfies() {
        let dir = tmp("overlay");
        let kernel = Kernel::new("sess_overlay", "/tmp/ws", "safe-edit", &dir).unwrap();
        let first = kernel
            .request(CapabilityRequest {
                capability: Capability::CommandExec,
                requested_by: carina_policy::Principal::Agent,
                resource: "npm install a".into(),
                session_id: "sess_overlay".into(),
                task_id: None,
            })
            .unwrap();
        assert_eq!(first.decision, Verdict::RequiresApproval);

        let approved = kernel
            .approve_for_session_with_justification(&first, "alice", "task needs npm dependency installs")
            .unwrap();
        assert_eq!(approved.decision, Verdict::Allowed);

        let second = kernel
            .request(CapabilityRequest {
                capability: Capability::CommandExec,
                requested_by: carina_policy::Principal::Agent,
                resource: "npm install b".into(),
                session_id: "sess_overlay".into(),
                task_id: None,
            })
            .unwrap();
        assert_eq!(second.decision, Verdict::Allowed);
        assert!(second.reason.contains("task needs npm dependency installs"));

        let events = kernel.audit().read_all().unwrap();
        assert!(events.iter().any(|e| {
            e.payload["status"] == "approval_overlay_created"
                && e.payload["justification"] == "task needs npm dependency installs"
        }));
        assert!(events.iter().any(|e| {
            e.payload["approval_overlay"]["resource_prefix"] == "npm"
                && e.payload["approval_overlay"]["source_decision_id"] == first.decision_id
        }));
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn approval_overlay_never_rescues_denied_decision() {
        let dir = tmp("overlay-deny");
        let kernel = Kernel::new("sess_overlay_deny", "/tmp/ws", "safe-edit", &dir).unwrap();
        let first = kernel
            .request(CapabilityRequest {
                capability: Capability::CommandExec,
                requested_by: carina_policy::Principal::Agent,
                resource: "rm old.txt".into(),
                session_id: "sess_overlay_deny".into(),
                task_id: None,
            })
            .unwrap();
        assert_eq!(first.decision, Verdict::RequiresApproval);
        kernel
            .approve_for_session_with_justification(&first, "alice", "allow simple rm during cleanup")
            .unwrap();

        let destructive = kernel
            .request(CapabilityRequest {
                capability: Capability::CommandExec,
                requested_by: carina_policy::Principal::Agent,
                resource: "rm -rf /".into(),
                session_id: "sess_overlay_deny".into(),
                task_id: None,
            })
            .unwrap();
        assert_eq!(destructive.decision, Verdict::Denied);
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn secret_request_records_handle_not_value() {
        let dir = tmp("secret");
        let mut kernel = Kernel::new("sess_s", "/tmp/ws", "full-workspace", &dir).unwrap();
        kernel.secrets_mut().grant("API_KEY", "plaintext-secret");
        let (decision, handle) = kernel.request_secret("API_KEY").unwrap();
        // full-workspace requires approval for secrets, so no handle yet.
        assert_eq!(decision.decision, Verdict::RequiresApproval);
        assert!(handle.is_none());
        // The audit log must not contain the plaintext.
        let raw = std::fs::read_to_string(kernel.audit().path()).unwrap();
        assert!(!raw.contains("plaintext-secret"));
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn export_audit_and_bundle_and_approval_policy() {
        let dir = tmp("export");
        let mut kernel = Kernel::new("sess_e", "/tmp/ws", "safe-edit", &dir).unwrap();
        kernel.set_bundle(PolicyBundle::default());
        kernel.set_approval_policy(ApprovalPolicy {
            required_role_at_risk: vec![(2, "lead".into())],
        });
        kernel.request_file_read("/etc/passwd", Some("task_1".into())).unwrap();
        let export = kernel.export_audit().unwrap();
        assert!(export["event_count"].as_u64().unwrap() >= 1);
        assert_eq!(export["profile"], "safe-edit");

        // Role-gated approval: without the role it is rejected.
        let d = kernel
            .request(CapabilityRequest {
                capability: Capability::CommandExec,
                requested_by: carina_policy::Principal::Agent,
                resource: "npm install x".into(),
                session_id: "sess_e".into(),
                task_id: None,
            })
            .unwrap();
        let rejected = kernel.approve_as(&d, "alice", None).unwrap();
        assert_eq!(rejected.decision, Verdict::Denied);
        let ok = kernel.approve_as(&d, "bob", Some("lead")).unwrap();
        assert_eq!(ok.decision, Verdict::Allowed);
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn approval_policy_required_role_thresholds() {
        let policy = ApprovalPolicy {
            required_role_at_risk: vec![(2, "lead".into()), (4, "security".into())],
        };
        assert_eq!(policy.required_role(1), None);
        assert_eq!(policy.required_role(2), Some("lead"));
        assert_eq!(policy.required_role(4), Some("security"));
        assert_eq!(policy.required_role(5), Some("security"));
    }
}
