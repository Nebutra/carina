//! Plugin runtime for Pi-OS (PRD §8.7).
//!
//! Plugins are WASM modules executed by the `wasmi` interpreter. They never
//! see the host filesystem, environment, or shell — the only way out is the
//! host import `pi_request_capability`, and every capability a plugin uses
//! must be declared in its manifest. An undeclared request is refused and
//! surfaced as a policy violation, so a plugin cannot exceed the permissions
//! shown to the user at install time.

mod host;
mod signing;

pub use host::{AllowDeclared, CapabilityHost, HostDecision, PluginRuntime, RunOutcome};
pub use signing::{SignatureVerifier, SigningError};

use serde::Deserialize;

#[derive(Debug, thiserror::Error)]
pub enum PluginError {
    #[error("invalid manifest: {0}")]
    Manifest(#[from] toml::de::Error),
    #[error("wasm error: {0}")]
    Wasm(String),
    #[error("plugin requested undeclared capability: {0}")]
    UndeclaredCapability(String),
}

/// Plugin manifest (PRD §8.7). Every permission the plugin can ever use must
/// be declared here and is shown to the user at install time.
#[derive(Debug, Clone, Deserialize)]
pub struct Manifest {
    pub name: String,
    pub version: String,
    #[serde(default)]
    pub kind: PluginKind,
    #[serde(default)]
    pub permissions: Permissions,
}

/// The plugin categories from PRD §8.7.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum PluginKind {
    #[default]
    Command,
    Tool,
    ModelProvider,
    Prompt,
    Policy,
    Ui,
    Workflow,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct Permissions {
    #[serde(default)]
    pub file_read: Vec<String>,
    #[serde(default)]
    pub file_write: Vec<String>,
    #[serde(default)]
    pub command_exec: Vec<String>,
    #[serde(default)]
    pub network: Vec<String>,
    #[serde(default)]
    pub secret: Vec<String>,
}

impl Manifest {
    pub fn from_toml(source: &str) -> Result<Self, PluginError> {
        Ok(toml::from_str(source)?)
    }

    /// Human-readable permission summary displayed at install time.
    pub fn permission_summary(&self) -> Vec<String> {
        let p = &self.permissions;
        let mut out = Vec::new();
        let mut push = |label: &str, values: &[String]| {
            if !values.is_empty() {
                out.push(format!("{label}: {}", values.join(", ")));
            }
        };
        push("file read", &p.file_read);
        push("file write", &p.file_write);
        push("command exec", &p.command_exec);
        push("network", &p.network);
        push("secrets", &p.secret);
        if out.is_empty() {
            out.push("no permissions requested".into());
        }
        out
    }

    /// Reports whether a capability request is covered by the declared
    /// permissions. `capability` is one of file_read/file_write/
    /// command_exec/network/secret; `resource` is checked against the
    /// declared list (exact match, or "workspace"/"*" wildcards).
    pub fn declares(&self, capability: &str, resource: &str) -> bool {
        let list = match capability {
            "file_read" => &self.permissions.file_read,
            "file_write" => &self.permissions.file_write,
            "command_exec" => &self.permissions.command_exec,
            "network" => &self.permissions.network,
            "secret" => &self.permissions.secret,
            _ => return false,
        };
        list.iter().any(|d| d == resource || d == "*" || d == "workspace")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const EXAMPLE: &str = r#"
name = "example-plugin"
version = "0.1.0"
kind = "tool"

[permissions]
file_read = ["workspace"]
file_write = ["patch_only"]
command_exec = ["npm test", "pytest"]
network = ["api.example.com"]
secret = []
"#;

    #[test]
    fn parses_prd_example_manifest() {
        let m = Manifest::from_toml(EXAMPLE).unwrap();
        assert_eq!(m.name, "example-plugin");
        assert_eq!(m.kind, PluginKind::Tool);
        assert_eq!(m.permissions.command_exec, vec!["npm test", "pytest"]);
        assert!(m.permissions.secret.is_empty());
    }

    #[test]
    fn declares_respects_manifest() {
        let m = Manifest::from_toml(EXAMPLE).unwrap();
        assert!(m.declares("command_exec", "npm test"));
        assert!(m.declares("file_read", "anything")); // workspace wildcard
        assert!(!m.declares("secret", "API_KEY"));
        assert!(!m.declares("network", "evil.example.com"));
    }

    #[test]
    fn zero_permission_manifest_is_valid() {
        let m = Manifest::from_toml("name = \"bare\"\nversion = \"0.0.1\"").unwrap();
        assert_eq!(m.permission_summary(), vec!["no permissions requested"]);
    }
}
