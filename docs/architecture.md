# Architecture

Carina is a layered agent runtime. Each layer has one job, one language, and one contract with the layer below it.

## Layers

| Layer | Language | Role | One-liner |
|-------|----------|------|-----------|
| Agent Surface | Go (in-daemon) | LLM interaction: agent loop, prompts, reasoner backends | makes it useful |
| Client Surfaces | TypeScript / Python / Go | CLI, TUI, IDE, web, SDKs — renderers over JSON-RPC | makes it usable |
| Control Plane | Go | daemon, RPC, sessions, scheduler, workers, model routing | makes it run |
| Capability Kernel | Rust | permissions, policy, audit, transactional patches, WASM plugins | makes it safe |
| Native Toolchain | Zig | scan, grep, diff, patch, process runner, pty | makes it sharp |

```
Client Surfaces ──JSON-RPC──▶ Go Control Plane (agent loop) ──Capability API──▶ Rust Kernel ──Native Calls──▶ Zig Tools
```

## Core principles

1. **Agents never touch system resources directly.** Every file read, command execution, network access, secret read, or persistent memory write is a capability request.
2. **Every side effect goes through the Capability Kernel.** The kernel evaluates the request against the session's permission profile and records a `PermissionDecision`.
3. **Every execution writes to the Event Log.** Append-only, timestamped, session-scoped. Sessions are replayable from the log alone.
4. **Every patch is a transaction.** Proposed → Validated → Approved → Applied → Verified → Committed, with a rollback pointer at every stage. No half-applied state, ever.
5. **Every tool declares its permissions.** Plugins and tools carry manifests; undeclared capability use is a `PolicyViolation` event.
6. **Local-first.** The default execution topology is one on-demand runtime per active workspace, with durable background work and obligation-aware idle shutdown.
7. **The CLI is a client.** `carina` resolves the workspace before it chooses configuration, state, or transport, then talks JSON-RPC to that workspace runtime. IDEs, CI, and SDKs use the same protocol.
8. **Cloud identity and sync are product boundaries.** Multi-endpoint identity,
   device registration, and sync belong to Nebutra Cloud (`nebutra.com`); the
   local runtime remains the authority for repository actions.

## Component map

### Go Control Plane (`go/`, `apps/`)

- `go/daemon` — long-running runtime host: lifecycle, unix-socket RPC listener, recovery, agent loop + reasoner backends, governed local memory.
- `go/localruntime` — canonical workspace identity, stable workspace/runtime IDs, coherent runtime specs, descriptors, mode decisions, and passive registry scanning.
- `go/localdaemon` — atomic connect-or-start, detached process launch, identity handshake, owner verification, and graceful stop signalling.
- `go/rpc` — JSON-RPC 2.0 server; method registry mirrors `protocol/jsonrpc`.
- `go/session-store` — session state + append-only JSONL event log (storage: JSON state + JSONL; SQLite is used by the `carina-index` code-intelligence crate).
- `go/scheduler` — task queue: submit / cancel / pause / resume, priorities, concurrency.
- `go/worker` — worker pool: local, remote, CI, sandbox workers.
- `go/model-router` — unified model call interface: provider fallback, rate limits, token usage log, streaming.
- `go/kernel` — bridge to the Rust capability kernel service.
- `go/channels` — trusted external event injection (HMAC-signed senders, dedup).
- `go/artifact` — retention-tiered tool-output artifact store.
- `go/extensions` — local marketplace: manifest validation, install/enable lifecycle.
- `go/mcp` / `go/mcpserver` — governed MCP manager and server.
- `go/contextengine` — context assembly and compaction support.
- `go/telemetry` — versioned newline-JSON telemetry and cost attribution.
- `go/runtimecontract` — runtime protocol contracts.
- `go/tui` / `go/workflowui` / `go/agentview` — terminal client engine, workflow run views, live agent views.
- `apps/carina-daemon` — daemon entrypoint.
- `apps/carina-cli` — user-facing CLI (`carina run`, `carina audit`, `carina patch …`).
- `apps/carina-cli` interactive shell (bare `carina`, optional shell flags) — live session/agent views plus in-terminal approval and question round-trips over the same JSON-RPC protocol (`go/tui` + `go/tuiapp`).
- `apps/carina-worker` — worker entrypoint.

### Workspace runtime topology

Bare `carina` and governed CLI commands resolve the canonical workspace before
loading project configuration or selecting state and socket paths. Workspace
runtime metadata lives under `~/.carina/runtimes/v1/<workspace-id>/`; the
macOS-safe socket name lives under `~/.carina/run/v1/`. The stable runtime ID
survives process restarts while each process receives a new epoch.

Clients do not trust reachability alone. They call `runtime.describe` and
`runtime.initialize` and validate mode, workspace ID/root, runtime ID, epoch,
socket, state root, and config fingerprint before session operations. A
reachable endpoint with a mismatched identity fails closed.

The directory tree is also a read-only runtime registry. No machine-global
supervisor is required: `carina runtimes` scans persisted specs/descriptors and
does not start a process. The old `~/.carina/daemon.sock` and
`~/.carina/state` layout remains available only through explicit legacy mode;
Carina never moves, merges, rewrites, or deletes that state automatically.

### Rust Capability Kernel (`crates/`)

- `carina-kernel` — capability types, capability requests, kernel façade that every side effect flows through.
- `carina-policy` — policy engine + permission profiles (`read-only`, `safe-edit`, `full-workspace`, `ci-runner`, …), workspace path containment, command risk classification, `MemoryWrite` and `SubagentSpawn` policy.
- `carina-patch` — transactional patch engine: lifecycle state machine, conflict detection, atomic apply, rollback pointers, provenance.
- `carina-audit` — event model (36 event types; `protocol/events/events.json` is authoritative), append-only audit log, report generation.
- `carina-index` — code-intelligence index (SQLite-backed).
- `carina-plugin-runtime` — WASM plugin host: manifest parsing, permission review, capability-scoped host functions.

### Zig Native Toolchain (`zig/`)

Small, fast, cross-platform binaries that emit machine-readable JSON and never bypass kernel policy:

`carina-scan` (workspace file tree), `carina-grep` (structured search), `carina-diff` (structured diff), `carina-patch-native` (apply/verify/rollback/dry-run), `carina-run` (command execution with timeout/env allowlist), `carina-pty` (interactive terminal sessions).

### Protocol (`protocol/`)

- `jsonrpc/` — method registry (Session, Task, Workspace, Capability, Worker APIs).
- `schemas/` — JSON Schemas for Task, Event, PermissionDecision, PatchTransaction, Session, Workspace, Channel.
- `events/` — the event type enumeration.
- `capabilities/` — capability types and built-in permission profiles.

## Governed memory

Carina's local long-term memory belongs to the control plane, not the prompt
builder. The daemon stores bounded entries under its state directory and keeps
two targets: `memory` for project/agent notes and `user` for profile facts.
Each agent run receives a frozen memory snapshot in the prompt, so memory writes
during that run persist for future work without changing the current run's
stable prefix.

Memory mutation is still a capability-mediated side effect. The daemon requests
`MemoryWrite` from the Rust kernel using a resource string that contains only
target, scope, action, operation count, and content hash. Built-in policy
requires approval by default. If approved, the daemon applies
add/replace/remove/batch changes atomically after local content scanning and
size checks. Audit records the decision and hash metadata, not raw memory text.

## MVP loop (Phase 1 target)

```
user prompt → Go daemon creates session → Agent Surface calls model
→ model requests FileRead → Rust kernel checks policy → Zig scans/reads
→ model proposes patch → Rust kernel opens PatchTransaction → user approves
→ Zig carina-patch applies → Go daemon runs tests → kernel checks CommandExec
→ Zig carina-run executes → Event Log records everything → user inspects / rolls back
```

## Communication & storage decisions

- **IPC (MVP):** JSON-RPC 2.0 over stdio / unix socket. gRPC / Cap'n Proto / FlatBuffers deferred — do not optimize the protocol before the loop closes.
- **Storage (MVP):** JSON state + JSONL event log + file snapshots (SQLite backs the `carina-index` code-intelligence crate). RocksDB / content-addressed storage deferred.
- **Plugins (MVP):** WASM with manifest-declared permissions; WASI capability model and signed packages deferred.

## Performance targets

| Operation | Target |
|-----------|--------|
| CLI cold start | < 100ms |
| CLI warm start | < 30ms |
| Workspace scan (10k files) | < 1s |
| Workspace scan (100k files) | < 5s |
| Grep (medium repo) | < 300ms |
| Patch apply (single file) | < 50ms |
| Patch apply (multi-file) | < 300ms |
| Event streaming end-to-end | < 100ms |
| Daemon crash recovery | < 3s |
