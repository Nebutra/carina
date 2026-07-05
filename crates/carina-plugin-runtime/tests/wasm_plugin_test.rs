//! Verifies the WASM plugin permission boundary (PRD §8.7 acceptance):
//! plugins cannot exceed their declared permissions, every request is
//! recorded, and an undeclared capability is refused.

use carina_plugin_runtime::{AllowDeclared, CapabilityHost, Manifest, PluginRuntime};

const MANIFEST: &str = r#"
name = "demo-plugin"
version = "0.1.0"
kind = "tool"

[permissions]
command_exec = ["npm test"]
"#;

// A plugin that:
//  1. stores two capability/resource strings in memory,
//  2. requests "command_exec"/"npm test"  (declared → should be allowed),
//  3. requests "secret"/"API_KEY"         (undeclared → must be denied),
//  4. returns the number of allowed requests.
//
// Memory layout (data segment):
//   0:  "command_exec"      (12 bytes)
//   16: "npm test"          (8 bytes)
//   32: "secret"            (6 bytes)
//   48: "API_KEY"           (7 bytes)
const WAT: &str = r#"
(module
  (import "env" "pi_request_capability"
    (func $req (param i32 i32 i32 i32) (result i32)))
  (import "env" "pi_log" (func $log (param i32 i32)))
  (memory (export "memory") 1)
  (data (i32.const 0)  "command_exec")
  (data (i32.const 16) "npm test")
  (data (i32.const 32) "secret")
  (data (i32.const 48) "API_KEY")
  (func (export "pi_run") (result i32)
    (local $allowed i32)
    ;; declared: command_exec / npm test
    (if (call $req (i32.const 0) (i32.const 12) (i32.const 16) (i32.const 8))
      (then (local.set $allowed (i32.add (local.get $allowed) (i32.const 1)))))
    ;; undeclared: secret / API_KEY  -> denied by host
    (if (call $req (i32.const 32) (i32.const 6) (i32.const 48) (i32.const 7))
      (then (local.set $allowed (i32.add (local.get $allowed) (i32.const 1)))))
    (call $log (i32.const 0) (i32.const 12))
    (local.get $allowed)))
"#;

#[test]
fn plugin_cannot_exceed_declared_permissions() {
    let wasm = wat::parse_str(WAT).expect("compile wat");
    let manifest = Manifest::from_toml(MANIFEST).unwrap();
    let runtime = PluginRuntime::new();

    let outcome = runtime
        .run(&manifest, &wasm, Box::new(AllowDeclared))
        .expect("plugin runs");

    // Exactly one request (the declared one) was allowed.
    assert_eq!(outcome.result_code, 1, "only the declared capability should be allowed");
    assert_eq!(outcome.decisions.len(), 2);

    let allowed: Vec<_> = outcome.decisions.iter().filter(|d| d.allowed).collect();
    assert_eq!(allowed.len(), 1);
    assert_eq!(allowed[0].capability, "command_exec");

    let denied: Vec<_> = outcome.decisions.iter().filter(|d| !d.allowed).collect();
    assert_eq!(denied.len(), 1);
    assert_eq!(denied[0].capability, "secret");
    assert!(denied[0].reason.contains("undeclared"));

    // The plugin logged its module id.
    assert!(outcome.logs.iter().any(|l| l == "command_exec"));
}

// A host that additionally vetoes a specific resource even if declared,
// proving the two gates (manifest + live policy) compose.
struct VetoNetwork;
impl CapabilityHost for VetoNetwork {
    fn allow(&self, capability: &str, _resource: &str) -> bool {
        capability != "command_exec" // veto the very thing the manifest allows
    }
}

#[test]
fn session_policy_can_veto_a_declared_capability() {
    let wasm = wat::parse_str(WAT).expect("compile wat");
    let manifest = Manifest::from_toml(MANIFEST).unwrap();
    let runtime = PluginRuntime::new();

    let outcome = runtime.run(&manifest, &wasm, Box::new(VetoNetwork)).unwrap();
    // Even though the manifest declares command_exec, live policy vetoes it,
    // so nothing is allowed.
    assert_eq!(outcome.result_code, 0);
    assert!(outcome.decisions.iter().all(|d| !d.allowed));
}
