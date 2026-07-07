# Architecture

Carina is a layered agent runtime. Each layer has one job, one language, and one contract with the layer below it.

## Layers

| Layer | Language | Role | One-liner |
|-------|----------|------|-----------|
| Agent Surface | TypeScript (initially) | LLM interaction, prompts, skills, UX | makes it useful |
| Control Plane | Go | daemon, RPC, sessions, scheduler, workers, model routing | makes it run |
| Capability Kernel | Rust | permissions, policy, audit, transactional patches, WASM plugins | makes it safe |
| Native Toolchain | Zig | scan, grep, diff, patch, process runner, pty | makes it sharp |

```
Agent Surface ──JSON-RPC──▶ Go Control Plane ──Capability API──▶ Rust Kernel ──Native Calls──▶ Zig Tools
```

## Core principles

1. **Agents never touch system resources directly.** Every file read, command execution, network access, or secret read is a capability request.
2. **Every side effect goes through the Capability Kernel.** The kernel evaluates the request against the session's permission profile and records a `PermissionDecision`.
3. **Every execution writes to the Event Log.** Append-only, timestamped, session-scoped. Sessions are replayable from the log alone.
4. **Every patch is a transaction.** Proposed → Validated → Approved → Applied → Verified → Committed, with a rollback pointer at every stage. No half-applied state, ever.
5. **Every tool declares its permissions.** Plugins and tools carry manifests; undeclared capability use is a `PolicyViolation` event.
6. **Local-first.** The daemon, workers, and remote execution are extensions — a single binary on a laptop is the base case.
7. **The CLI is a client.** `carina` talks JSON-RPC to the daemon. IDEs, CI, and SDKs use the same protocol.
8. **Cloud identity and sync are product boundaries.** Multi-endpoint identity,
   device registration, and sync belong to Nebutra Cloud (`nebutra.com`); the
   local runtime remains the authority for repository actions.

## Component map

### Go Control Plane (`go/`, `apps/`)

- `go/daemon` — long-running runtime host: lifecycle, unix-socket RPC listener, recovery.
- `go/rpc` — JSON-RPC 2.0 server; method registry mirrors `protocol/jsonrpc`.
- `go/session-store` — session state + append-only JSONL event log (MVP storage: SQLite + JSONL).
- `go/scheduler` — task queue: submit / cancel / pause / resume, priorities, concurrency.
- `go/worker` — worker pool: local, remote, CI, sandbox workers.
- `go/model-router` — unified model call interface: provider fallback, rate limits, token usage log, streaming.
- `apps/carina-daemon` — daemon entrypoint.
- `apps/carina-cli` — user-facing CLI (`carina run`, `carina audit`, `carina patch …`).
- `apps/carina-tui` — interactive TUI (Phase 1+).

### Rust Capability Kernel (`crates/`)

- `carina-kernel` — capability types, capability requests, kernel façade that every side effect flows through.
- `carina-policy` — policy engine + permission profiles (`read-only`, `safe-edit`, `full-workspace`, `ci-runner`, …), workspace path containment, command risk classification.
- `carina-patch` — transactional patch engine: lifecycle state machine, conflict detection, atomic apply, rollback pointers, provenance.
- `carina-audit` — event model (20 event types), append-only audit log, report generation.
- `carina-plugin-runtime` — WASM plugin host: manifest parsing, permission review, capability-scoped host functions.

### Zig Native Toolchain (`zig/`)

Small, fast, cross-platform binaries that emit machine-readable JSON and never bypass kernel policy:

`carina-scan` (workspace file tree), `carina-grep` (structured search), `carina-diff` (structured diff), `carina-patch-native` (apply/verify/rollback/dry-run), `carina-run` (command execution with timeout/env allowlist), `carina-pty` (interactive terminal sessions).

### Protocol (`protocol/`)

- `jsonrpc/` — method registry (Session, Task, Workspace, Capability, Worker APIs).
- `schemas/` — JSON Schemas for Task, Event, PermissionDecision, PatchTransaction, Session, Workspace.
- `events/` — the event type enumeration.
- `capabilities/` — capability types and built-in permission profiles.

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
- **Storage (MVP):** SQLite + JSONL event log + file snapshots. RocksDB / content-addressed storage deferred.
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
