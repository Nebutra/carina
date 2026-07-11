//! Policy engine for the Carina Capability Kernel (PRD §8.3, §8.8).
//!
//! Every side effect in the runtime is a [`CapabilityRequest`] evaluated
//! against the session's [`Profile`], producing a [`Decision`] that is
//! recorded in the audit log by `carina-kernel`.

use serde::{Deserialize, Serialize};
use std::path::{Component, Path, PathBuf};

/// The capability types (protocol/capabilities/capabilities.json).
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
    MemoryWrite,
    CodeIndex,
    ContextCompress,
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
        // CodeIndex is derived FileRead access (the per-workspace index DB
        // stores file content and outlives sessions), so a bundle constraint
        // on FileRead constrains index queries the same way — a stricter
        // session must not read content through an index a looser one built.
        if req.capability == Capability::CodeIndex
            && self.deny_capabilities.contains(&Capability::FileRead)
        {
            return (
                Verdict::Denied,
                format!("org policy '{}' denies FileRead (the code index is derived read access)", self.name),
            );
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
        // Escalate an auto-allow to require approval if the bundle demands it
        // (for CodeIndex also when it demands approval for FileRead — see above).
        if decision == Verdict::Allowed
            && (self.require_approval.contains(&req.capability)
                || (req.capability == Capability::CodeIndex
                    && self.require_approval.contains(&Capability::FileRead)))
        {
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
                // Secret-bearing files are denied even inside the workspace
                // (PRD §13.8): they must go through the secret broker.
                if is_sensitive_file(&req.resource) {
                    return (
                        Verdict::Denied,
                        "sensitive file (credentials/secret material) — use the secret broker".into(),
                    );
                }
                if let Err(e) = guard_special_path(&req.resource) {
                    return (Verdict::Denied, e);
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
                    if let Err(e) = guard_special_path(&req.resource) {
                        return (Verdict::Denied, e);
                    }
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
            Capability::PluginLoad => {
                // Plugins and MCP tool loads are mediated (approval), not denied
                // outright — the plugin runtime and MCP client are implemented.
                (Verdict::RequiresApproval, "plugin/MCP load requires approval".into())
            }
            Capability::RemoteExecute => {
                (Verdict::Denied, "remote execution is not enabled for this profile".into())
            }
            Capability::MemoryWrite => (
                Verdict::RequiresApproval,
                "persistent memory write requires approval".into(),
            ),
            Capability::CodeIndex => {
                // Derived read access: ingestion is FileRead-gated per path, so
                // the index follows the profile's FileRead grant here; bundle
                // FileRead constraints are applied in `PolicyBundle::constrain`
                // (the per-workspace DB outlives sessions, so the *querying*
                // session's read policy must hold too, not just the ingesting one's).
                if profile.file_read_in_workspace {
                    (Verdict::Allowed, format!("{} allows FileRead within workspace", profile.name))
                } else {
                    (Verdict::Denied, "profile denies FileRead".into())
                }
            }
            Capability::ContextCompress => {
                // The managed Headroom adapter is a session-scoped, reversible
                // transform (compressed output always carries an OriginalRef/
                // OriginalSHA256 back to the untouched content — see
                // context_compression.go). It is allowed by default so routine
                // per-observation compression doesn't stall on approval, but the
                // request is still policy-evaluated and audited like every other
                // capability: an org PolicyBundle can still tighten this to
                // RequiresApproval or Denied (`evaluate_with_bundle` only ever
                // tightens, never loosens).
                (
                    Verdict::Allowed,
                    "context compression is a reversible, session-scoped internal adapter".into(),
                )
            }
        }
    }
}

/// True if the path names credential/secret material that must never be
/// read as a plain file, even inside the workspace (PRD §13.8).
pub fn is_sensitive_file(path: &str) -> bool {
    let lower = path.to_ascii_lowercase();
    let base = std::path::Path::new(&lower)
        .file_name()
        .and_then(|s| s.to_str())
        .unwrap_or(&lower);

    // Exact/base-name matches for secret files.
    let sensitive_names = [
        ".env", ".npmrc", ".pypirc", ".netrc", ".git-credentials",
        "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa", "credentials",
    ];
    if sensitive_names.iter().any(|n| base == *n || base.starts_with(".env.")) {
        return true;
    }
    // Path-segment matches for secret directories.
    let sensitive_dirs = ["/.ssh/", "/.aws/", "/.gnupg/", "/.config/gcloud", "/.kube/"];
    sensitive_dirs.iter().any(|d| lower.contains(d))
}

/// guard_special_path rejects paths that name a device/pseudo file, a special
/// node (FIFO/socket/char-or-block device), a Windows UNC/device namespace, or
/// contain a NUL byte — none of which a workspace file op should touch, even if
/// they lexically resolve inside the workspace (e.g. a symlink to /dev/tcp).
pub fn guard_special_path(path: &str) -> Result<(), String> {
    if path.contains('\0') {
        return Err("path contains a NUL byte".into());
    }
    if path.starts_with(r"\\") {
        return Err("Windows UNC/device path is not allowed".into());
    }
    let lower = path.to_ascii_lowercase();
    let device = ["/dev/", "/proc/", "/sys/", "/run/"];
    if device.iter().any(|p| lower.starts_with(p)) || lower == "/dev" || lower == "/proc" {
        return Err(format!("path targets a device/pseudo filesystem: {path}"));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::FileTypeExt;
        if let Ok(md) = std::fs::symlink_metadata(path) {
            let ft = md.file_type();
            if ft.is_fifo() || ft.is_socket() || ft.is_char_device() || ft.is_block_device() {
                return Err("path is a special node (fifo/socket/device)".into());
            }
        }
    }
    Ok(())
}

/// Approval mode — the "when do you stop and ask me" axis, orthogonal to the
/// permission profile's "what can you do" axis (Codex's key insight). This
/// lets the same capability set run at different interruption levels.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum ApprovalMode {
    /// Only known-safe (read-only) commands auto-run; everything else asks.
    Untrusted,
    /// Profile decides; risky effects ask. (default)
    #[default]
    OnRequest,
    /// Never ask — deny stands, but requires-approval becomes allow.
    Never,
}

impl ApprovalMode {
    pub fn from_str(s: &str) -> ApprovalMode {
        match s {
            "untrusted" => ApprovalMode::Untrusted,
            "never" => ApprovalMode::Never,
            _ => ApprovalMode::OnRequest,
        }
    }
}

/// A command with no side effects (risk level 0) is "known safe".
pub fn is_readonly_command(command: &str) -> bool {
    classify_command(command) == 0
}

/// Applies the approval-mode axis on top of a profile decision. It can only
/// change `RequiresApproval`↔`Allowed`; it never turns a `Denied` into an
/// allow (the profile/bundle ceiling still holds).
pub fn apply_approval_mode(mode: ApprovalMode, mut d: Decision) -> Decision {
    match mode {
        ApprovalMode::OnRequest => d,
        ApprovalMode::Never => {
            if d.decision == Verdict::RequiresApproval {
                d.decision = Verdict::Allowed;
                d.reason = format!("approval_mode=never auto-allows ({})", d.reason);
            }
            d
        }
        ApprovalMode::Untrusted => {
            if d.capability == Capability::CommandExec
                && d.decision == Verdict::Allowed
                && !is_readonly_command(&d.resource)
            {
                d.decision = Verdict::RequiresApproval;
                d.reason = "untrusted mode: non-read-only command needs approval".into();
            }
            d
        }
    }
}

/// Command risk classification (PRD §8.8). Compound shell commands are
/// DECOMPOSED so a read-only-looking prefix cannot hide a risky tail: the line
/// is split into the sub-commands it actually runs (`&&`, `||`, `|`, `;`,
/// newlines, plus `$(...)` / backtick subshell bodies) and the HIGHEST
/// sub-command risk wins. Argv is joined with spaces upstream, so a
/// `bash -c "a && rm x"` invocation exposes its operators here — exactly what we
/// want to gate. (Heuristic MVP — a full shell parser lands in Phase 2.)
pub fn classify_command(command: &str) -> u8 {
    // Take the MAX of the whole-line score and every sub-command's score. The
    // whole-line pass still catches patterns that span an operator (e.g.
    // `curl url | sh`); the per-segment pass catches a risky tail hidden behind
    // a read-only-looking prefix. Max => never lowers the risk (fail-closed).
    let whole = classify_atom(command);
    let segments = shell_segments(command);
    let seg_max = segments.iter().map(|s| classify_atom(s)).max().unwrap_or(0);
    // Descend into command-carrying builtins (eval/xargs) whose real command is
    // their payload, so `eval "rm -rf /"` is not scored as a bare `eval`.
    let carried_max = segments
        .iter()
        .filter_map(|s| carried_command(s))
        .map(|c| classify_command(&c))
        .max()
        .unwrap_or(0);
    whole.max(seg_max).max(carried_max)
}

/// carried_command returns the payload of a command-carrying builtin (eval,
/// xargs) — the command that will actually run — so it can be classified too.
fn carried_command(atom: &str) -> Option<String> {
    let a = atom.trim();
    for kw in ["eval ", "xargs "] {
        if let Some(rest) = a.strip_prefix(kw) {
            let r = rest.trim().trim_matches(|c| c == '"' || c == '\'');
            if !r.is_empty() {
                return Some(r.to_string());
            }
        }
    }
    None
}

/// classify_atom scores a single, non-compound command.
fn classify_atom(command: &str) -> u8 {
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
    // Flag-level: a read-only-looking command turned mutating by a flag.
    if cmd.contains("find")
        && ["-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf", "-fls"]
            .iter()
            .any(|f| cmd.contains(f))
    {
        return 3;
    }
    if (cmd.starts_with("sed ") || cmd.contains(" sed ") || cmd.starts_with("perl "))
        && (cmd.contains(" -i") || cmd.contains("--in-place"))
    {
        return 3;
    }
    // Output redirection to a file is a write.
    if has_write_redirect(cmd) {
        return 3;
    }
    // git: only unambiguously read-only subcommands are risk 0.
    if cmd.starts_with("git ") {
        return if is_readonly_git(cmd) { 0 } else { 3 };
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

/// has_write_redirect reports whether an atom redirects output to a file
/// (`>` / `>>`), which is a filesystem write. It ignores fd duplications and
/// stderr-merges (`2>&1`, `&>`, `->`, `=>`, `<>`).
fn has_write_redirect(cmd: &str) -> bool {
    let b = cmd.as_bytes();
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'>' {
            let prev = if i > 0 { b[i - 1] } else { b' ' };
            let next = if i + 1 < b.len() { b[i + 1] } else { b' ' };
            if prev == b'&' || prev == b'-' || prev == b'=' || prev == b'<' || next == b'&' {
                i += 1;
                continue;
            }
            return true;
        }
        i += 1;
    }
    false
}

/// is_readonly_git reports whether a `git <sub>` command is unambiguously
/// read-only. Mutating-capable subcommands (branch/tag/config/remote/commit/…)
/// are deliberately excluded so they are NOT scored as risk 0.
fn is_readonly_git(cmd: &str) -> bool {
    let sub = cmd
        .strip_prefix("git ")
        .unwrap_or("")
        .split_whitespace()
        .next()
        .unwrap_or("");
    matches!(
        sub,
        "status"
            | "log"
            | "diff"
            | "show"
            | "rev-parse"
            | "describe"
            | "ls-files"
            | "ls-tree"
            | "blame"
            | "cat-file"
            | "symbolic-ref"
            | "shortlog"
            | "reflog"
            | "whatchanged"
            | "grep"
    )
}

/// shell_segments splits a command line into the sub-commands it will run,
/// separating on top-level `&&`, `||`, `|`, `;`, and newlines (quote-aware so
/// operators inside quotes are ignored), and pulling out `$(...)` and backtick
/// subshell bodies as their own segments. Being over-eager here is fail-closed:
/// a mis-split only raises the risk estimate, never lowers it.
fn shell_segments(command: &str) -> Vec<String> {
    let chars: Vec<char> = command.chars().collect();
    let mut segments: Vec<String> = Vec::new();
    let mut subshells: Vec<String> = Vec::new();
    let mut cur = String::new();
    let (mut sq, mut dq) = (false, false);
    let mut i = 0;
    while i < chars.len() {
        let c = chars[i];
        if c == '\'' && !dq {
            sq = !sq;
            cur.push(c);
            i += 1;
            continue;
        }
        if c == '"' && !sq {
            dq = !dq;
            cur.push(c);
            i += 1;
            continue;
        }
        if !sq && !dq {
            if c == '$' && i + 1 < chars.len() && chars[i + 1] == '(' {
                let mut depth = 1;
                let mut j = i + 2;
                let mut inner = String::new();
                while j < chars.len() && depth > 0 {
                    match chars[j] {
                        '(' => depth += 1,
                        ')' => {
                            depth -= 1;
                            if depth == 0 {
                                break;
                            }
                        }
                        _ => {}
                    }
                    if depth > 0 {
                        inner.push(chars[j]);
                    }
                    j += 1;
                }
                subshells.push(inner);
                i = j + 1;
                continue;
            }
            if c == '`' {
                let mut j = i + 1;
                let mut inner = String::new();
                while j < chars.len() && chars[j] != '`' {
                    inner.push(chars[j]);
                    j += 1;
                }
                subshells.push(inner);
                i = j + 1;
                continue;
            }
            // Process substitution <(...) and >(...): the body runs as a command.
            if (c == '<' || c == '>') && i + 1 < chars.len() && chars[i + 1] == '(' {
                let mut depth = 1;
                let mut j = i + 2;
                let mut inner = String::new();
                while j < chars.len() && depth > 0 {
                    match chars[j] {
                        '(' => depth += 1,
                        ')' => {
                            depth -= 1;
                            if depth == 0 {
                                break;
                            }
                        }
                        _ => {}
                    }
                    if depth > 0 {
                        inner.push(chars[j]);
                    }
                    j += 1;
                }
                subshells.push(inner);
                i = j + 1;
                continue;
            }
            if c == ';' || c == '\n' {
                segments.push(std::mem::take(&mut cur));
                i += 1;
                continue;
            }
            if c == '&' {
                // `&&` and background `&` are both boundaries; `&>` is a redirect.
                if i + 1 < chars.len() && chars[i + 1] == '&' {
                    segments.push(std::mem::take(&mut cur));
                    i += 2;
                    continue;
                }
                if i + 1 < chars.len() && chars[i + 1] == '>' {
                    cur.push(c);
                    i += 1;
                    continue;
                }
                if i > 0 && chars[i - 1] == '>' {
                    cur.push(c); // `>&` fd duplication (e.g. 2>&1), not a boundary
                    i += 1;
                    continue;
                }
                segments.push(std::mem::take(&mut cur));
                i += 1;
                continue;
            }
            if c == '|' {
                let step = if i + 1 < chars.len() && chars[i + 1] == '|' { 2 } else { 1 };
                segments.push(std::mem::take(&mut cur));
                i += step;
                continue;
            }
        }
        cur.push(c);
        i += 1;
    }
    segments.push(cur);

    let mut out: Vec<String> = Vec::new();
    for s in segments {
        let t = s.trim();
        if !t.is_empty() {
            out.push(t.to_string());
        }
    }
    for sub in subshells {
        for s in shell_segments(&sub) {
            out.push(s);
        }
    }
    out
}

#[cfg(test)]
mod shell_gating_tests {
    use super::*;

    #[test]
    fn simple_commands_unchanged() {
        assert_eq!(classify_command("echo hello"), 0);
        assert_eq!(classify_command("grep foo ."), 0);
        assert_eq!(classify_command("cargo test"), 1);
        assert_eq!(classify_command("rm -rf /tmp/x"), 5);
        assert_eq!(classify_command("git push origin main"), 4);
    }

    #[test]
    fn compound_takes_max_sub_command_risk() {
        // The bypass: a read-only prefix used to hide a mutating tail (risk 0).
        assert_eq!(classify_command("cat f && chmod 777 x"), 3);
        assert_eq!(classify_command("ls && mv a b"), 3);
        assert_eq!(classify_command("echo ok; rm secret.txt"), 3);
        assert_eq!(classify_command("echo ok && rm -rf /tmp/x"), 5);
        assert_eq!(classify_command("ls -la | git push"), 4);
        // Read-only chains stay read-only.
        assert_eq!(classify_command("echo a && echo b"), 0);
        assert_eq!(classify_command("cat f | grep x | wc -l"), 0);
    }

    #[test]
    fn subshell_bodies_are_gated() {
        assert_eq!(classify_command("echo $(rm -rf /tmp/x)"), 5);
        assert_eq!(classify_command("echo `chmod 777 y`"), 3);
    }

    #[test]
    fn operators_inside_quotes_are_not_split() {
        // echoing a literal string that contains && must stay read-only.
        assert_eq!(classify_command("echo \"a && b\""), 0);
        assert_eq!(classify_command("grep 'a|b' file"), 0);
    }

    #[test]
    fn hidden_mutation_is_no_longer_read_only() {
        assert!(!is_readonly_command("cat f && rm x"));
        assert!(is_readonly_command("cat f && echo done"));
    }

    #[test]
    fn background_process_sub_and_eval_boundaries() {
        // single '&' background operator is a boundary (tail was hidden before)
        assert_eq!(classify_command("echo done & chmod 777 x"), 3);
        assert_eq!(classify_command("echo a & echo b"), 0);
        // '&>' redirect is NOT a background boundary
        assert_eq!(classify_command("echo hi &> out.txt"), 0);
        // process substitution bodies are gated (a 'cat' prefix no longer hides them)
        assert_eq!(classify_command("cat <(chmod 777 y)"), 3);
        // command-carrying builtins descend into their payload
        assert_eq!(classify_command("eval \"rm -rf /tmp/x\""), 5);
        assert_eq!(classify_command("xargs chmod 777 < list"), 3);
    }

    #[test]
    fn dangerous_flags_redirects_and_git_writes() {
        // find/sed flags that mutate
        assert_eq!(classify_command("find . -delete"), 3);
        assert_eq!(classify_command("find . -exec rm {} ;"), 3);
        assert_eq!(classify_command("find . -name '*.rs'"), 0); // benign find stays read-only
        assert_eq!(classify_command("sed -i s/a/b/ f.txt"), 3);
        // output redirection is a write; fd redirection is not
        assert_eq!(classify_command("echo pwned > /etc/hosts"), 3);
        assert_eq!(classify_command("cat f > out.txt"), 3);
        assert_eq!(classify_command("ls -la 2>&1"), 0);
        // git: read-only subcommands are 0, mutating ones are not
        assert_eq!(classify_command("git status"), 0);
        assert_eq!(classify_command("git show HEAD"), 0);
        assert_eq!(classify_command("git commit -am x"), 3);
        assert_eq!(classify_command("git branch -D main"), 3);
    }

    #[test]
    fn special_paths_are_denied() {
        assert!(guard_special_path("/dev/tcp/1.2.3.4/80").is_err());
        assert!(guard_special_path("/proc/self/mem").is_err());
        assert!(guard_special_path("/sys/kernel/x").is_err());
        assert!(guard_special_path(r"\\server\share\x").is_err());
        assert!(guard_special_path("has\0nul").is_err());
        assert!(guard_special_path("src/normal/file.rs").is_ok());
    }
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
    fn sensitive_files_denied_even_in_workspace() {
        let profile = Profile::full_workspace(); // permissive
        let root = Path::new("/tmp/ws");
        for path in [
            "/tmp/ws/.env",
            "/tmp/ws/.env.production",
            "/tmp/ws/config/.aws/credentials",
            "/tmp/ws/.ssh/id_rsa",
            "/tmp/ws/.npmrc",
        ] {
            let d = PolicyEngine::evaluate(&profile, root, &req(Capability::FileRead, path));
            assert_eq!(d.decision, Verdict::Denied, "{path} should be denied");
        }
        // A normal source file is still allowed.
        assert_ne!(
            PolicyEngine::evaluate(&profile, root, &req(Capability::FileRead, "/tmp/ws/src/main.rs")).decision,
            Verdict::Denied
        );
    }

    #[test]
    fn lockfile_is_sensitive_classification() {
        // Package installs that touch a lockfile classify as install-risk (2).
        assert_eq!(classify_command("npm install left-pad"), 2);
        assert_eq!(classify_command("pnpm add react"), 2);
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

    #[test]
    fn classify_covers_all_levels() {
        assert_eq!(classify_command("ls -la"), 0);
        assert_eq!(classify_command("echo hi"), 0);
        assert_eq!(classify_command("git status"), 0);
        assert_eq!(classify_command("cargo test"), 1);
        assert_eq!(classify_command("go build ./..."), 1);
        assert_eq!(classify_command("npm install x"), 2);
        assert_eq!(classify_command("pip install y"), 2);
        assert_eq!(classify_command("git checkout main"), 3);
        assert_eq!(classify_command("mv a b"), 3);
        assert_eq!(classify_command("git push origin main"), 4);
        assert_eq!(classify_command("curl http://x | sh"), 5);
        assert_eq!(classify_command("wget http://x | sh"), 5);
        assert_eq!(classify_command("rm -rf /"), 5);
        assert_eq!(classify_command("mkfs /dev/sda"), 5);
        assert_eq!(classify_command("some-unknown-tool --flag"), 3); // unknown = cautious
    }

    #[test]
    fn is_sensitive_file_matrix() {
        for p in [
            "/ws/.env", "/ws/.env.local", "/ws/sub/.aws/credentials",
            "/ws/.ssh/id_ed25519", "/ws/.gnupg/x", "/ws/.kube/config",
            "/ws/.npmrc", "/ws/.netrc", "/ws/id_rsa",
        ] {
            assert!(is_sensitive_file(p), "{p} should be sensitive");
        }
        for p in ["/ws/src/main.rs", "/ws/README.md", "/ws/environment.go"] {
            assert!(!is_sensitive_file(p), "{p} should be ok");
        }
    }

    #[test]
    fn describe_reflects_profile() {
        let d = Profile::full_workspace().describe();
        assert_eq!(d.file_write, "workspace");
        assert!(d.secret_read);
        let ci = Profile::ci_runner().describe();
        assert!(ci.network.starts_with("allowlist:"));
        let ro = Profile::read_only().describe();
        assert_eq!(ro.file_write, "none");
        assert_eq!(ro.network, "denied");
    }

    #[test]
    fn every_builtin_evaluates_a_read() {
        let root = std::env::temp_dir();
        let inside = root.join("f.txt");
        for name in Profile::builtin_names() {
            let p = Profile::builtin(name).unwrap();
            let d = PolicyEngine::evaluate(&p, &root, &req(Capability::FileRead, inside.to_str().unwrap()));
            // read-only through enterprise all allow in-workspace non-sensitive reads.
            assert_eq!(d.decision, Verdict::Allowed, "{name} should allow a normal read");
        }
    }

    #[test]
    fn from_toml_network_allowlist_and_secret() {
        let p = Profile::from_toml(
            "name = \"x\"\n[rules]\nnetwork_access = { allow = \"allowlist\" }\nsecret_read = { allow = \"scoped\" }\n[network_allowlist]\nhosts = [\"api.x.com\"]\n",
        )
        .unwrap();
        assert!(p.secret_read);
        let root = Path::new("/tmp/ws");
        assert_eq!(
            PolicyEngine::evaluate(&p, root, &req(Capability::NetworkAccess, "api.x.com")).decision,
            Verdict::Allowed
        );
        assert_eq!(
            PolicyEngine::evaluate(&p, root, &req(Capability::NetworkAccess, "evil.com")).decision,
            Verdict::Denied
        );
    }

    #[test]
    fn bundle_network_host_and_require_approval() {
        let bundle = PolicyBundle::from_toml(
            "name = \"b\"\ndeny_network_hosts = [\"bad.com\"]\nrequire_approval = [\"CommandExec\"]\n",
        )
        .unwrap();
        let profile = Profile::full_workspace();
        let root = Path::new("/tmp/ws");
        assert_eq!(
            PolicyEngine::evaluate_with_bundle(&profile, Some(&bundle), root, &req(Capability::NetworkAccess, "bad.com")).decision,
            Verdict::Denied
        );
        // require_approval escalates an auto-allowed command to approval.
        let d = PolicyEngine::evaluate_with_bundle(&profile, Some(&bundle), root, &req(Capability::CommandExec, "ls"));
        assert_eq!(d.decision, Verdict::RequiresApproval);
    }

    #[test]
    fn memory_write_is_scoped_and_policy_bundle_controllable() {
        let profile = Profile::safe_edit();
        let root = Path::new("/tmp/ws");
        let resource = "target=memory scope=abcd action=add ops=1 content_sha256=01";
        let base = PolicyEngine::evaluate(&profile, root, &req(Capability::MemoryWrite, resource));
        assert_eq!(base.decision, Verdict::RequiresApproval);

        let never = apply_approval_mode(ApprovalMode::Never, base.clone());
        assert_eq!(never.decision, Verdict::Allowed);

        let deny_bundle =
            PolicyBundle::from_toml("name = \"locked\"\ndeny_capabilities = [\"MemoryWrite\"]\n").unwrap();
        let denied = PolicyEngine::evaluate_with_bundle(
            &profile,
            Some(&deny_bundle),
            root,
            &req(Capability::MemoryWrite, resource),
        );
        assert_eq!(denied.decision, Verdict::Denied);
    }

    #[test]
    fn code_index_is_derived_read_access() {
        let root = Path::new("/tmp/ws");
        // Allowed iff the profile allows in-workspace FileRead (all builtins do).
        for name in Profile::builtin_names() {
            let p = Profile::builtin(name).unwrap();
            let d = PolicyEngine::evaluate(&p, root, &req(Capability::CodeIndex, "index search foo"));
            assert_eq!(d.decision, Verdict::Allowed, "{name} should allow CodeIndex");
        }
        // A custom profile that denies FileRead also denies the index.
        let mut no_read = Profile::read_only();
        no_read.file_read_in_workspace = false;
        let d = PolicyEngine::evaluate(&no_read, root, &req(Capability::CodeIndex, "index search foo"));
        assert_eq!(d.decision, Verdict::Denied);
    }

    #[test]
    fn code_index_follows_bundle_file_read_constraints() {
        // CodeIndex is derived FileRead access: the per-workspace index DB
        // outlives sessions, so a bundle that denies or escalates FileRead
        // must constrain index queries too — otherwise content a session
        // cannot FileRead is served through code.search.
        let root = Path::new("/tmp/ws");
        let profile = Profile::safe_edit();

        let deny = PolicyBundle::from_toml("name = \"locked\"\ndeny_capabilities = [\"FileRead\"]\n").unwrap();
        let d = PolicyEngine::evaluate_with_bundle(
            &profile,
            Some(&deny),
            root,
            &req(Capability::CodeIndex, "index search secret"),
        );
        assert_eq!(d.decision, Verdict::Denied, "FileRead deny must deny the index: {}", d.reason);
        assert!(d.reason.contains("locked"));

        let escalate =
            PolicyBundle::from_toml("name = \"strict\"\nrequire_approval = [\"FileRead\"]\n").unwrap();
        let d = PolicyEngine::evaluate_with_bundle(
            &profile,
            Some(&escalate),
            root,
            &req(Capability::CodeIndex, "index search secret"),
        );
        assert_eq!(
            d.decision,
            Verdict::RequiresApproval,
            "FileRead escalation must escalate the index: {}",
            d.reason
        );
    }

    #[test]
    fn code_index_bundle_deny_and_require_approval() {
        let root = Path::new("/tmp/ws");
        let profile = Profile::safe_edit();
        let deny = PolicyBundle::from_toml("name = \"locked\"\ndeny_capabilities = [\"CodeIndex\"]\n").unwrap();
        let d = PolicyEngine::evaluate_with_bundle(
            &profile,
            Some(&deny),
            root,
            &req(Capability::CodeIndex, "index build files=3"),
        );
        assert_eq!(d.decision, Verdict::Denied);
        assert!(d.reason.contains("locked"));

        let escalate =
            PolicyBundle::from_toml("name = \"strict\"\nrequire_approval = [\"CodeIndex\"]\n").unwrap();
        let d = PolicyEngine::evaluate_with_bundle(
            &profile,
            Some(&escalate),
            root,
            &req(Capability::CodeIndex, "index map budget=1024"),
        );
        assert_eq!(d.decision, Verdict::RequiresApproval);
    }

    #[test]
    fn approval_mode_is_orthogonal_to_profile() {
        let profile = Profile::safe_edit();
        let root = Path::new("/tmp/ws");

        // safe-edit: a package install is risk-2 => RequiresApproval by profile.
        let base = PolicyEngine::evaluate(&profile, root, &req(Capability::CommandExec, "npm install x"));
        assert_eq!(base.decision, Verdict::RequiresApproval);

        // never: the same decision becomes Allowed (deny would still stand).
        let never = apply_approval_mode(ApprovalMode::Never, base.clone());
        assert_eq!(never.decision, Verdict::Allowed);

        // never must NOT rescue an outright deny.
        let denied = PolicyEngine::evaluate(&profile, root, &req(Capability::CommandExec, "rm -rf /"));
        assert_eq!(apply_approval_mode(ApprovalMode::Never, denied).decision, Verdict::Denied);

        // untrusted: an allowed non-read-only command is escalated to approval;
        // a read-only one stays allowed.
        let cargo = PolicyEngine::evaluate(&profile, root, &req(Capability::CommandExec, "cargo test"));
        assert_eq!(cargo.decision, Verdict::Allowed); // allowlisted
        let untrusted = apply_approval_mode(ApprovalMode::Untrusted, cargo);
        assert_eq!(untrusted.decision, Verdict::RequiresApproval); // cargo test isn't read-only (risk 1)

        let ls = Decision {
            decision_id: "perm_x".into(),
            capability: Capability::CommandExec,
            requested_by: Principal::Agent,
            resource: "ls -la".into(),
            decision: Verdict::Allowed,
            reason: "ok".into(),
            policy_id: "p".into(),
        };
        assert_eq!(apply_approval_mode(ApprovalMode::Untrusted, ls).decision, Verdict::Allowed);
    }

    #[test]
    fn capabilities_that_need_approval_or_denied() {
        let p = Profile::full_workspace();
        let root = Path::new("/tmp/ws");
        assert_eq!(
            PolicyEngine::evaluate(&p, root, &req(Capability::GitOperation, "push")).decision,
            Verdict::RequiresApproval
        );
        assert_eq!(
            PolicyEngine::evaluate(&p, root, &req(Capability::PluginLoad, "x")).decision,
            Verdict::RequiresApproval
        );
        assert_eq!(
            PolicyEngine::evaluate(&p, root, &req(Capability::RemoteExecute, "x")).decision,
            Verdict::Denied
        );
        assert_eq!(
            PolicyEngine::evaluate(&p, root, &req(Capability::ProcessSpawn, "x")).decision,
            Verdict::RequiresApproval
        );
    }
}
