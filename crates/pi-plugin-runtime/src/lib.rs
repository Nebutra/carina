//! Plugin runtime for Pi-OS (PRD §8.7).
//!
//! Phase 0 delivers manifest parsing and permission review. The WASM
//! execution engine (wasmtime + capability-scoped host functions) lands in
//! Phase 4 — plugins never see the host filesystem, environment, or shell;
//! they only call back into the kernel through [`CapabilityHost`].

use pi_policy::{CapabilityRequest, Decision};
use serde::Deserialize;

#[derive(Debug, thiserror::Error)]
pub enum PluginError {
    #[error("invalid manifest: {0}")]
    Manifest(#[from] toml::de::Error),
    #[error("plugin requested undeclared capability: {0}")]
    UndeclaredCapability(String),
}

/// Plugin manifest (PRD §8.7). Every permission the plugin can ever use
/// must be declared here and is shown to the user at install time.
#[derive(Debug, Clone, Deserialize)]
pub struct Manifest {
    pub name: String,
    pub version: String,
    #[serde(default)]
    pub permissions: Permissions,
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
}

/// The only door out of a plugin: capability requests brokered by the
/// kernel. Implemented by `pi-kernel` on the host side.
pub trait CapabilityHost {
    fn request(&self, req: CapabilityRequest) -> Decision;
}

#[cfg(test)]
mod tests {
    use super::*;

    const EXAMPLE: &str = r#"
name = "example-plugin"
version = "0.1.0"

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
        assert_eq!(m.permissions.command_exec, vec!["npm test", "pytest"]);
        assert!(m.permissions.secret.is_empty());
    }

    #[test]
    fn zero_permission_manifest_is_valid() {
        let m = Manifest::from_toml("name = \"bare\"\nversion = \"0.0.1\"").unwrap();
        assert_eq!(m.permission_summary(), vec!["no permissions requested"]);
    }
}
