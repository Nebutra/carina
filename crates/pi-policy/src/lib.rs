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

    pub fn full_workspace() -> Self {
        Self {
            name: "full-workspace".into(),
            file_read_in_workspace: true,
            file_write: WriteMode::Workspace,
            command_allowlist: vec![],
            max_command_risk: 3,
            network: NetworkMode::RequiresApproval,
            secret_read: true,
        }
    }

    pub fn ci_runner() -> Self {
        Self {
            name: "ci-runner".into(),
            file_read_in_workspace: true,
            file_write: WriteMode::PatchOnly,
            command_allowlist: [
                "npm ci", "npm test", "npm run build", "cargo build", "cargo test",
                "go build ./...", "go test ./...", "make", "make test",
            ]
            .iter()
            .map(|s| s.to_string())
            .collect(),
            max_command_risk: 2,
            network: NetworkMode::Allowlist(vec![
                "registry.npmjs.org".into(),
                "crates.io".into(),
                "proxy.golang.org".into(),
            ]),
            secret_read: true, // scoped; the broker still gates per-secret
        }
    }

    pub fn sandboxed() -> Self {
        Self {
            name: "sandboxed".into(),
            file_read_in_workspace: true,
            file_write: WriteMode::PatchOnly,
            command_allowlist: vec![],
            max_command_risk: 0,
            network: NetworkMode::Denied,
            secret_read: false,
        }
    }

    pub fn trusted_local() -> Self {
        Self {
            name: "trusted-local".into(),
            file_read_in_workspace: true,
            file_write: WriteMode::Workspace,
            command_allowlist: vec![],
            max_command_risk: 4,
            network: NetworkMode::RequiresApproval,
            secret_read: true,
        }
    }

    pub fn enterprise_restricted() -> Self {
        Self {
            name: "enterprise-restricted".into(),
            file_read_in_workspace: true,
            file_write: WriteMode::PatchOnly,
            command_allowlist: vec![],
            max_command_risk: 1,
            network: NetworkMode::Denied,
            secret_read: false,
        }
    }

    /// All seven built-in profiles by name (PRD §8.3).
    pub fn builtin(name: &str) -> Option<Self> {
        match name {
            "read-only" => Some(Self::read_only()),
            "safe-edit" => Some(Self::safe_edit()),
            "full-workspace" => Some(Self::full_workspace()),
            "ci-runner" => Some(Self::ci_runner()),
            "sandboxed" => Some(Self::sandboxed()),
            "trusted-local" => Some(Self::trusted_local()),
            "enterprise-restricted" => Some(Self::enterprise_restricted()),
            _ => None,
        }
    }

    pub fn builtin_names() -> &'static [&'static str] {
        &[
            "read-only", "safe-edit", "full-workspace", "ci-runner",
            "sandboxed", "trusted-local", "enterprise-restricted",
        ]
    }

    /// Loads a custom profile from the TOML format used in
    /// protocol/capabilities/profiles/*.toml (PRD §8.3: users can define
    /// custom permission profiles).
    pub fn from_toml(source: &str) -> Result<Self, ProfileError> {
        let raw: RawProfile = toml::from_str(source).map_err(|e| ProfileError(e.to_string()))?;
        let rules = raw.rules.unwrap_or_default();

        let file_write = match rules.file_write.allow.as_deref() {
            Some("patch_only") => WriteMode::PatchOnly,
            Some("workspace") => WriteMode::Workspace,
            _ => WriteMode::Denied,
        };
        let network = match rules.network_access.allow.as_deref() {
            Some("approval") => NetworkMode::RequiresApproval,
            Some("allowlist") => {
                NetworkMode::Allowlist(raw.network_allowlist.map(|a| a.hosts).unwrap_or_default())
            }
            _ => NetworkMode::Denied,
        };
        Ok(Self {
            name: raw.name,
            file_read_in_workspace: matches!(rules.file_read.allow.as_deref(), Some("workspace") | None),
            file_write,
            command_allowlist: raw.command_allowlist.map(|a| a.patterns).unwrap_or_default(),
            max_command_risk: rules.command_exec.max_risk_level.unwrap_or(0),
            network,
            secret_read: !matches!(rules.secret_read.allow.as_deref(), Some("none") | None),
        })
    }

    /// A machine-readable description of what this profile grants
    /// (the "capability graph" view, PRD §8.3).
    pub fn describe(&self) -> ProfileDescription {
        ProfileDescription {
            name: self.name.clone(),
            file_read: if self.file_read_in_workspace { "workspace" } else { "none" }.into(),
            file_write: match self.file_write {
                WriteMode::Denied => "none",
                WriteMode::PatchOnly => "patch_only",
                WriteMode::Workspace => "workspace",
            }
            .into(),
            max_command_risk: self.max_command_risk,
            command_allowlist: self.command_allowlist.clone(),
            network: match &self.network {
                NetworkMode::Denied => "denied".into(),
                NetworkMode::RequiresApproval => "approval".into(),
                NetworkMode::Allowlist(h) => format!("allowlist:{}", h.join(",")),
            },
            secret_read: self.secret_read,
        }
    }
}

#[derive(Debug)]
pub struct ProfileError(pub String);

impl std::fmt::Display for ProfileError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "invalid profile: {}", self.0)
    }
}
impl std::error::Error for ProfileError {}

#[derive(Debug, Clone, Serialize)]
pub struct ProfileDescription {
    pub name: String,
    pub file_read: String,
    pub file_write: String,
    pub max_command_risk: u8,
    pub command_allowlist: Vec<String>,
    pub network: String,
    pub secret_read: bool,
}

#[derive(Debug, Deserialize)]
struct RawProfile {
    name: String,
    rules: Option<RawRules>,
    command_allowlist: Option<RawAllowlist>,
    network_allowlist: Option<RawNetworkAllowlist>,
}

/// Each rule in the TOML is an inline table, e.g.
/// `command_exec = { allow = "allowlist", max_risk_level = 1 }`.
#[derive(Debug, Default, Deserialize)]
struct Rule {
    allow: Option<String>,
    max_risk_level: Option<u8>,
}

#[derive(Debug, Default, Deserialize)]
struct RawRules {
    #[serde(default)]
    file_read: Rule,
    #[serde(default)]
    file_write: Rule,
    #[serde(default)]
    command_exec: Rule,
    #[serde(default)]
    network_access: Rule,
    #[serde(default)]
    secret_read: Rule,
}

#[derive(Debug, Deserialize)]
struct RawAllowlist {
    #[serde(default)]
    patterns: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct RawNetworkAllowlist {
    #[serde(default)]
    hosts: Vec<String>,
}

/// An organization-wide policy bundle (PRD §5 Phase 5: team policy). Its
/// rules are *mandatory* — they can only tighten a session's profile, never
/// loosen it. A deny here overrides any allow the profile would grant.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct PolicyBundle {
    #[serde(default)]
    pub name: String,
    /// Capabilities that are always denied, regardless of profile.
    #[serde(default)]
    pub deny_capabilities: Vec<Capability>,
    /// A hard ceiling on command risk; commands above this are denied.
    #[serde(default)]
    pub max_command_risk: Option<u8>,
    /// Network hosts that are always denied.
    #[serde(default)]
    pub deny_network_hosts: Vec<String>,
    /// Capabilities that always require human approval even if the profile
    /// would auto-allow them.
    #[serde(default)]
    pub require_approval: Vec<Capability>,
}

impl PolicyBundle {
    pub fn from_toml(source: &str) -> Result<Self, ProfileError> {
        toml::from_str(source).map_err(|e| ProfileError(e.to_string()))
    }

    /// Applies the bundle to a profile decision, only ever tightening it.
    fn constrain(&self, req: &CapabilityRequest, decision: Verdict, reason: String) -> (Verdict, String) {
        if self.deny_capabilities.contains(&req.capability) {
            return (Verdict::Denied, format!("org policy '{}' denies {:?}", self.name, req.capability));
        }
        if req.capability == Capability::NetworkAccess
            && self.deny_network_hosts.iter().any(|h| h == &req.resource)
        {
            return (Verdict::Denied, format!("org policy '{}' blocks host {}", self.name, req.resource));
        }
        if req.capability == Capability::CommandExec {
            if let Some(cap) = self.max_command_risk {
                if classify_command(&req.resource) > cap {
                    return (
                        Verdict::Denied,
                        format!("org policy '{}' caps command risk at {}", self.name, cap),
                    );
                }
            }
        }
        // Escalate an auto-allow to require approval if the bundle demands it.
        if decision == Verdict::Allowed && self.require_approval.contains(&req.capability) {
            return (
                Verdict::RequiresApproval,
                format!("org policy '{}' requires approval for {:?}", self.name, req.capability),
            );
        }
        (decision, reason)
    }
}

/// Stateless policy evaluation. The kernel owns audit + approval flow.
pub struct PolicyEngine;

impl PolicyEngine {
    pub fn evaluate(profile: &Profile, workspace_root: &Path, req: &CapabilityRequest) -> Decision {
        Self::evaluate_with_bundle(profile, None, workspace_root, req)
    }

    /// Evaluates against the profile, then applies the org policy bundle
    /// (which can only tighten the result).
    pub fn evaluate_with_bundle(
        profile: &Profile,
        bundle: Option<&PolicyBundle>,
        workspace_root: &Path,
        req: &CapabilityRequest,
    ) -> Decision {
        let policy_id = format!("policy_{}", profile.name.replace('-', "_"));
        let (mut decision, mut reason) = Self::verdict(profile, workspace_root, req);
        if let Some(bundle) = bundle {
            let (d, r) = bundle.constrain(req, decision, reason);
            decision = d;
            reason = r;
        }
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

    #[test]
    fn all_seven_builtins_resolve() {
        for name in Profile::builtin_names() {
            assert!(Profile::builtin(name).is_some(), "missing builtin {name}");
        }
        assert!(Profile::builtin("nonexistent").is_none());
    }

    #[test]
    fn custom_profile_loads_from_toml() {
        let src = r#"
name = "my-team"
description = "custom"

[rules]
file_read = { allow = "workspace" }
file_write = { allow = "patch_only" }
command_exec = { allow = "allowlist", max_risk_level = 2 }
network_access = { allow = "allowlist" }
secret_read = { allow = "scoped" }

[command_allowlist]
patterns = ["make deploy-staging"]

[network_allowlist]
hosts = ["internal.example.com"]
"#;
        let p = Profile::from_toml(src).unwrap();
        assert_eq!(p.name, "my-team");
        assert_eq!(p.max_command_risk, 2);
        assert!(p.secret_read);
        assert_eq!(p.file_write, WriteMode::PatchOnly);
        assert!(matches!(p.network, NetworkMode::Allowlist(ref h) if h == &["internal.example.com"]));
        assert!(p.command_allowlist.contains(&"make deploy-staging".to_string()));
    }

    #[test]
    fn enterprise_restricted_denies_network_and_secrets() {
        let p = Profile::enterprise_restricted();
        let root = Path::new("/tmp/ws");
        assert_eq!(
            PolicyEngine::evaluate(&p, root, &req(Capability::NetworkAccess, "example.com")).decision,
            Verdict::Denied
        );
        assert_eq!(
            PolicyEngine::evaluate(&p, root, &req(Capability::SecretRead, "TOKEN")).decision,
            Verdict::Denied
        );
    }

    #[test]
    fn policy_bundle_only_tightens() {
        let bundle = PolicyBundle::from_toml(
            r#"
name = "acme-corp"
deny_capabilities = ["SecretRead"]
max_command_risk = 0
require_approval = ["PatchApply"]
"#,
        )
        .unwrap();
        let profile = Profile::full_workspace(); // permissive
        let root = Path::new("/tmp/ws");

        // A command the profile would allow is capped by the bundle.
        let d = PolicyEngine::evaluate_with_bundle(
            &profile,
            Some(&bundle),
            root,
            &req(Capability::CommandExec, "cargo build"),
        );
        assert_eq!(d.decision, Verdict::Denied);
        assert!(d.reason.contains("acme-corp"));

        // Secrets: profile allows (as approval), bundle denies outright.
        let d = PolicyEngine::evaluate_with_bundle(
            &profile,
            Some(&bundle),
            root,
            &req(Capability::SecretRead, "TOKEN"),
        );
        assert_eq!(d.decision, Verdict::Denied);
    }

    #[test]
    fn policy_bundle_cannot_loosen() {
        // An empty bundle must not turn a profile deny into an allow.
        let bundle = PolicyBundle::default();
        let profile = Profile::read_only();
        let d = PolicyEngine::evaluate_with_bundle(
            &profile,
            Some(&bundle),
            Path::new("/tmp/ws"),
            &req(Capability::CommandExec, "cargo test"),
        );
        assert_ne!(d.decision, Verdict::Allowed);
    }
}
