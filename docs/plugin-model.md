# Plugin Model

Plugins extend the runtime without breaking the security boundary. Execution target: **WASM** (Phase 4), hosted by `crates/carina-plugin-runtime`.

## Plugin types

| Type | Extends |
|------|---------|
| Command Plugin | user-facing commands |
| Tool Plugin | agent-callable tools |
| Model Provider Plugin | model-router backends |
| Prompt Plugin | prompt templates / skills |
| Policy Plugin | custom policy rules |
| UI Plugin | TUI/IDE surfaces |
| Workflow Plugin | multi-step orchestrations |

## Hard rules

1. Plugins cannot access the host filesystem directly — only through capability API host functions.
2. Plugins cannot read environment variables.
3. Plugins cannot execute shell commands directly.
4. Plugin permissions are displayed at install time and recorded.
5. Every plugin action is written to the event log, attributed to the plugin.

## Manifest

Every plugin ships a manifest declaring its full permission surface:

```toml
name = "example-plugin"
version = "0.1.0"

[permissions]
file_read = ["workspace"]
file_write = ["patch_only"]
command_exec = ["npm test", "pytest"]
network = ["api.example.com"]
secret = []
```

Undeclared capability use is rejected by the kernel and logged as `PolicyViolation`.

## Lifecycle

install (manifest review + permission display) → load (`PluginLoad` capability check) → run (capability-scoped host calls, audited) → uninstall.

Signed plugin packages are enforced today: when any key is present in the policy directory's `trusted-keys`, every module must carry a valid ed25519 signature before instantiation (see [docs/enterprise.md §3](enterprise.md)). A remote plugin registry is future work.
