//! Policy engine for the Pi-OS Capability Kernel (PRD §8.3, §8.8).
//!
//! Every side effect in the runtime is a [`CapabilityRequest`] evaluated
//! against the session's [`Profile`], producing a [`Decision`] that is
//! recorded in the audit log by `pi-kernel`.

use serde::{Deserialize, Serialize};
use std::path::{Component, Path, PathBuf};

/// The ten capability types (protocol/capabilities/capabilities.json).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum Capability {
    FileRead,
    FileWrite,
    CommandExec,
    NetworkAccess,
    SecretRead,
    GitOperation,
    PatchApply,
    ProcessSpawn,
    PluginLoad,
    RemoteExecute,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Principal {
    Agent,
    Plugin,
    User,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Verdict {
    Allowed,
    Denied,
    RequiresApproval,
}

/// A request to perform a side effect.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CapabilityRequest {
    pub capability: Capability,
    pub requested_by: Principal,
    /// Path, command line, host name, or secret name.
    pub resource: String,
    pub session_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task_id: Option<String>,
}

/// Mirrors protocol/schemas/permission-decision.schema.json.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Decision {
    pub decision_id: String,
    pub capability: Capability,
    pub requested_by: Principal,
    pub resource: String,
    pub decision: Verdict,
    pub reason: String,
    pub policy_id: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum WriteMode {
    Denied,
    PatchOnly,
    Workspace,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum NetworkMode {
    Denied,
    RequiresApproval,
    Allowlist(Vec<String>),
}

/// A permission profile (protocol/capabilities/profiles/*.toml).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Profile {
    pub name: String,
    pub file_read_in_workspace: bool,
    pub file_write: WriteMode,
    /// Exact command lines that are always allowed (subject to risk ceiling).
    pub command_allowlist: Vec<String>,
    /// Highest auto-allowed command risk level; anything above requires
    /// approval, and level 5 is always denied.
    pub max_command_risk: u8,
    pub network: NetworkMode,
    pub secret_read: bool,
}

impl Profile {
    pub fn read_only() -> Self {
        Self {
            name: "read-only".into(),
            file_read_in_workspace: true,
            file_write: WriteMode::Denied,
            command_allowlist: vec![],
            max_command_risk: 0,
            network: NetworkMode::Denied,
            secret_read: false,
        }
    }

    pub fn safe_edit() -> Self {
        Self {
            name: "safe-edit".into(),
            file_read_in_workspace: true,
            file_write: WriteMode::PatchOnly,
            command_allowlist: [
                "npm test", "pnpm test", "yarn test", "bun test", "cargo test",
                "cargo check", "go test ./...", "go vet ./...", "pytest",
                "make test", "zig build test", "tsc --noEmit",
            ]
            .iter()
            .map(|s| s.to_string())
            .collect(),
            max_command_risk: 1,
            network: NetworkMode::RequiresApproval,
            secret_read: false,
        }
    }

    pub fn builtin(name: &str) -> Option<Self> {
        match name {
            "read-only" => Some(Self::read_only()),
            "safe-edit" => Some(Self::safe_edit()),
            _ => None,
        }
    }
}

/// Stateless policy evaluation. The kernel owns audit + approval flow.
pub struct PolicyEngine;

impl PolicyEngine {
    pub fn evaluate(profile: &Profile, workspace_root: &Path, req: &CapabilityRequest) -> Decision {
        let policy_id = format!("policy_{}", profile.name.replace('-', "_"));
        let (decision, reason) = Self::verdict(profile, workspace_root, req);
        Decision {
            decision_id: new_decision_id(),
            capability: req.capability,
            requested_by: req.requested_by,
            resource: req.resource.clone(),
            decision,
            reason,
            policy_id,
        }
    }

    fn verdict(profile: &Profile, root: &Path, req: &CapabilityRequest) -> (Verdict, String) {
        match req.capability {
            Capability::FileRead => {
                if !profile.file_read_in_workspace {
                    return (Verdict::Denied, "profile denies FileRead".into());
                }
                if path_within_workspace(root, Path::new(&req.resource)) {
                    (Verdict::Allowed, format!("{} allows FileRead within workspace", profile.name))
                } else {
                    (Verdict::Denied, "path escapes workspace boundary".into())
                }
            }
            Capability::FileWrite => match profile.file_write {
                WriteMode::Denied => (Verdict::Denied, "profile denies FileWrite".into()),
                WriteMode::PatchOnly => (
                    Verdict::Denied,
                    "FileWrite only through PatchApply under this profile".into(),
                ),
                WriteMode::Workspace => {
                    if path_within_workspace(root, Path::new(&req.resource)) {
                        (Verdict::Allowed, "workspace write allowed".into())
                    } else {
                        (Verdict::Denied, "path escapes workspace boundary".into())
                    }
                }
            },
            Capability::PatchApply => match profile.file_write {
                WriteMode::Denied => (Verdict::Denied, "profile denies all writes".into()),
                _ => (Verdict::RequiresApproval, "patch apply requires approval".into()),
            },
            Capability::CommandExec => {
                let risk = classify_command(&req.resource);
                if risk >= 5 {
                    return (Verdict::Denied, "destructive command (risk level 5) denied by default".into());
                }
                if profile.command_allowlist.iter().any(|c| c == &req.resource) {
                    return (Verdict::Allowed, format!("{} allowlist match", profile.name));
                }
                if risk <= profile.max_command_risk {
                    (Verdict::Allowed, format!("risk level {risk} within profile ceiling"))
                } else if risk >= 4 {
                    (Verdict::Denied, format!("risk level {risk} requires an explicit profile"))
                } else {
                    (Verdict::RequiresApproval, format!("risk level {risk} exceeds profile ceiling"))
                }
            }
            Capability::NetworkAccess => match &profile.network {
                NetworkMode::Denied => (Verdict::Denied, "profile denies network access".into()),
                NetworkMode::RequiresApproval => {
                    (Verdict::RequiresApproval, "network access requires approval".into())
                }
                NetworkMode::Allowlist(hosts) => {
                    if hosts.iter().any(|h| h == &req.resource) {
                        (Verdict::Allowed, "host on network allowlist".into())
                    } else {
                        (Verdict::Denied, "host not on network allowlist".into())
                    }
                }
            },
            Capability::SecretRead => {
                if profile.secret_read {
                    (Verdict::RequiresApproval, "secret access always requires approval".into())
                } else {
                    (Verdict::Denied, "profile denies SecretRead".into())
                }
            }
            Capability::GitOperation | Capability::ProcessSpawn => {
                (Verdict::RequiresApproval, "mediated operation requires approval".into())
            }
            Capability::PluginLoad | Capability::RemoteExecute => {
                (Verdict::Denied, "not available before Phase 3/4".into())
            }
        }
    }
}

/// Command risk classification (PRD §8.8). Heuristic MVP — a proper
/// classifier with shell parsing lands in Phase 2.
pub fn classify_command(command: &str) -> u8 {
    let cmd = command.trim();
    let destructive = ["rm -rf", "rm -fr", "mkfs", "dd if=", ":(){", "> /dev/"];
    if destructive.iter().any(|p| cmd.contains(p)) || (cmd.contains("curl") && cmd.contains("| sh"))
        || (cmd.contains("wget") && cmd.contains("| sh"))
    {
        return 5;
    }
    let network = ["git push", "deploy", "kubectl apply", "terraform apply", "curl ", "wget ", "ssh "];
    if network.iter().any(|p| cmd.starts_with(p) || cmd.contains(p)) {
        return 4;
    }
    let mutation = ["git checkout", "git reset", "mv ", "rm ", "chmod ", "chown "];
    if mutation.iter().any(|p| cmd.starts_with(p)) {
        return 3;
    }
    let install = ["npm install", "npm i ", "pnpm add", "yarn add", "pip install", "cargo add", "brew install", "go get"];
    if install.iter().any(|p| cmd.starts_with(p)) {
        return 2;
    }
    let build = ["npm test", "pnpm test", "cargo test", "cargo check", "cargo build", "go test", "go build", "go vet", "pytest", "make", "zig build", "tsc", "eslint", "prettier"];
    if build.iter().any(|p| cmd.starts_with(p)) {
        return 1;
    }
    let readonly = [
        "ls", "cat ", "git status", "git log", "git diff", "pwd", "which ", "head ", "tail ",
        "grep ", "echo ", "echo", "printf ", "true", "false", "env", "date", "wc ", "find ", "stat ",
    ];
    if readonly.iter().any(|p| cmd == *p || cmd.starts_with(p)) {
        return 0;
    }
    3 // unknown commands are treated as file-mutating until classified
}

/// Workspace boundary check with lexical normalization plus symlink
/// resolution (PRD §8.9: symlinks must not escape). Paths that do not exist
/// yet (e.g. a file about to be created) are resolved through their deepest
/// existing ancestor so `/var` vs `/private/var` style symlinks still match.
pub fn path_within_workspace(root: &Path, candidate: &Path) -> bool {
    let abs = if candidate.is_absolute() {
        candidate.to_path_buf()
    } else {
        root.join(candidate)
    };
    resolve_via_existing_ancestor(&abs).starts_with(resolve_via_existing_ancestor(root))
}

/// Canonicalizes the deepest existing ancestor of `path` and re-appends the
/// (lexically normalized) remainder.
fn resolve_via_existing_ancestor(path: &Path) -> PathBuf {
    let norm = lexical_normalize(path);
    let mut current = norm.as_path();
    let mut tail: Vec<std::ffi::OsString> = Vec::new();
    loop {
        if let Ok(resolved) = current.canonicalize() {
            let mut out = resolved;
            for component in tail.iter().rev() {
                out.push(component);
            }
            return out;
        }
        match (current.parent(), current.file_name()) {
            (Some(parent), Some(name)) => {
                tail.push(name.to_os_string());
                current = parent;
            }
            _ => return norm,
        }
    }
}

fn lexical_normalize(path: &Path) -> PathBuf {
    let mut out = PathBuf::new();
    for comp in path.components() {
        match comp {
            Component::ParentDir => {
                out.pop();
            }
            Component::CurDir => {}
            other => out.push(other),
        }
    }
    out
}

fn new_decision_id() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or_default();
    format!("perm_{nanos:x}")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn req(capability: Capability, resource: &str) -> CapabilityRequest {
        CapabilityRequest {
            capability,
            requested_by: Principal::Agent,
            resource: resource.into(),
            session_id: "sess_test".into(),
            task_id: None,
        }
    }

    #[test]
    fn read_outside_workspace_is_denied() {
        let profile = Profile::safe_edit();
        let d = PolicyEngine::evaluate(&profile, Path::new("/tmp/ws"), &req(Capability::FileRead, "/etc/passwd"));
        assert_eq!(d.decision, Verdict::Denied);
    }

    #[test]
    fn traversal_cannot_escape_workspace() {
        let profile = Profile::safe_edit();
        let d = PolicyEngine::evaluate(
            &profile,
            Path::new("/tmp/ws"),
            &req(Capability::FileRead, "/tmp/ws/../../etc/passwd"),
        );
        assert_eq!(d.decision, Verdict::Denied);
    }

    #[test]
    fn safe_edit_denies_secrets_and_direct_writes() {
        let profile = Profile::safe_edit();
        let root = Path::new("/tmp/ws");
        assert_eq!(
            PolicyEngine::evaluate(&profile, root, &req(Capability::SecretRead, "OPENAI_API_KEY")).decision,
            Verdict::Denied
        );
        assert_eq!(
            PolicyEngine::evaluate(&profile, root, &req(Capability::FileWrite, "/tmp/ws/a.ts")).decision,
            Verdict::Denied
        );
    }

    #[test]
    fn destructive_commands_denied_tests_allowed() {
        let profile = Profile::safe_edit();
        let root = Path::new("/tmp/ws");
        assert_eq!(
            PolicyEngine::evaluate(&profile, root, &req(Capability::CommandExec, "rm -rf /")).decision,
            Verdict::Denied
        );
        assert_eq!(
            PolicyEngine::evaluate(&profile, root, &req(Capability::CommandExec, "cargo test")).decision,
            Verdict::Allowed
        );
    }
}
