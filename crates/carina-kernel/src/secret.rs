//! Secret broker (PRD §12.3).
//!
//! Agents never read the environment. Secrets are registered with the
//! broker and handed out only as opaque *handles* (`secret://name`); the
//! plaintext never crosses the agent boundary and never enters the event
//! log. Command output is redacted against every known secret value before
//! it is streamed or logged.

use std::collections::HashMap;

/// Stores secret values and issues handles. One broker per session.
#[derive(Default)]
pub struct SecretBroker {
    /// name -> plaintext value
    secrets: HashMap<String, String>,
}

impl SecretBroker {
    pub fn new() -> Self {
        Self::default()
    }

    /// Registers a secret. The value is held only in memory.
    pub fn grant(&mut self, name: &str, value: &str) {
        self.secrets.insert(name.to_string(), value.to_string());
    }

    /// Returns the opaque handle for a secret if it exists. The plaintext
    /// is never returned to the agent.
    pub fn handle(&self, name: &str) -> Option<String> {
        if self.secrets.contains_key(name) {
            Some(format!("secret://{name}"))
        } else {
            None
        }
    }

    pub fn is_known(&self, name: &str) -> bool {
        self.secrets.contains_key(name)
    }

    pub fn names(&self) -> Vec<String> {
        self.secrets.keys().cloned().collect()
    }

    /// Resolves a handle back to plaintext for injection into a command
    /// environment. This is only ever called inside the kernel/runner, not
    /// exposed to agents or plugins.
    pub fn resolve(&self, handle: &str) -> Option<&str> {
        let name = handle.strip_prefix("secret://")?;
        self.secrets.get(name).map(String::as_str)
    }

    /// Redacts every known secret value from `text`, replacing it with
    /// `«redacted:NAME»`. Applied to command output before logging.
    pub fn redact(&self, text: &str) -> String {
        let mut out = text.to_string();
        for (name, value) in &self.secrets {
            if value.is_empty() {
                continue;
            }
            out = out.replace(value, &format!("«redacted:{name}»"));
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn handle_hides_plaintext() {
        let mut b = SecretBroker::new();
        b.grant("API_KEY", "sk-super-secret-123");
        assert_eq!(b.handle("API_KEY").as_deref(), Some("secret://API_KEY"));
        assert!(b.handle("MISSING").is_none());
        // The handle must not contain the plaintext.
        assert!(!b.handle("API_KEY").unwrap().contains("sk-super-secret"));
    }

    #[test]
    fn resolve_only_inside_kernel() {
        let mut b = SecretBroker::new();
        b.grant("TOKEN", "abc123");
        assert_eq!(b.resolve("secret://TOKEN"), Some("abc123"));
        assert_eq!(b.resolve("secret://NOPE"), None);
    }

    #[test]
    fn redaction_scrubs_command_output() {
        let mut b = SecretBroker::new();
        b.grant("DB_PASSWORD", "hunter2");
        let output = "connecting with password hunter2 to db";
        assert_eq!(b.redact(output), "connecting with password «redacted:DB_PASSWORD» to db");
    }
}
