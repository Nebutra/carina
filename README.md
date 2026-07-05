# Pi-OS — Pi Agent OS Runtime

> A local-first, security-controlled, extensible, remotely schedulable **Agent Runtime** for coding agents and general agents.

Pi-OS is not a rewrite of [pi](https://github.com/earendil-works/pi) — it is a redefinition of it: from *LLM CLI Tool* to **Agent OS Runtime**.

```
Go makes it run.
Rust makes it safe.
Zig makes it sharp.
LLM makes it useful.
```

## Architecture

```
┌─────────────────────────────────────────────┐
│              Agent Surface                  │
│  CLI / TUI / IDE / SDK / Prompt / Skills    │
└──────────────────────┬──────────────────────┘
                       │ JSON-RPC / gRPC
┌──────────────────────▼──────────────────────┐
│              Go Control Plane               │
│ daemon / scheduler / sessions / workers     │
│ model router / remote exec / observability  │
└──────────────────────┬──────────────────────┘
                       │ Capability API
┌──────────────────────▼──────────────────────┐
│            Rust Capability Kernel           │
│ policy / permission / audit / sandbox       │
│ transaction patch / event log / WASM plugins│
└──────────────────────┬──────────────────────┘
                       │ Native Tool Calls
┌──────────────────────▼──────────────────────┐
│              Zig Native Toolchain           │
│ scan / grep / diff / patch / pty / runner   │
└─────────────────────────────────────────────┘
```

### Core principles

1. Agents never touch system resources directly.
2. Every side effect goes through the Capability Kernel.
3. Every execution is written to the append-only Event Log.
4. Every patch is previewable, verifiable, and rollbackable.
5. Every tool capability declares its permissions explicitly.
6. Local-first by default; remote execution is an extension.
7. The CLI is a client — not the runtime.

## Repository layout

```
apps/        pi-cli / pi-tui / pi-daemon        (entrypoints)
crates/      pi-kernel / pi-policy / pi-patch / pi-audit / pi-plugin-runtime   (Rust)
zig/         pi-scan / pi-grep / pi-diff / pi-patch-native / pi-run / pi-pty   (Zig)
go/          daemon / scheduler / worker / rpc / model-router / session-store  (Go)
sdk/         typescript / python / go
protocol/    jsonrpc / schemas / events / capabilities
docs/        architecture / security-model / plugin-model / rpc-api / PRD
```

## Build

Toolchains: Go ≥ 1.25, Rust ≥ 1.85, Zig 0.15.x.

```bash
make go      # build Go control plane + apps
make rust    # cargo check the capability kernel crates
make zig     # build Zig native tools (requires zig)
make all
```

```bash
# Phase 0 smoke test
./bin/pi-daemon &          # control plane on ~/.pi-os/daemon.sock
./bin/pi status            # daemon health
./bin/pi run "fix tests"   # create session + submit task (TaskCreated → event log)
./bin/pi audit <session>   # replay the append-only event stream
./zig/zig-out/bin/pi-grep "TODO" src/main.ts   # structured JSON search
```

## Status

All six PRD phases (0–5) are implemented and tested, **and pi-os drives a real ReAct coding agent** — the model decides, the Rust kernel authorizes, the Zig tools execute, every step is audited and rollbackable. See [docs/agent.md](docs/agent.md) for how to run it, [docs/PRD.md](docs/PRD.md) for the full requirements, [docs/architecture.md](docs/architecture.md) for the layered design, and [docs/enterprise.md](docs/enterprise.md) for the Phase 5 controls.

```bash
cd your-repo && pi run "fix the failing test"   # Claude reasons; pi-os executes safely
```

**Tests:** 57 Rust tests (`cargo test --workspace`) + Go unit & end-to-end tests across all three languages (`go test ./...`). Run everything with `make test`.

**Coverage (measured):** Go overall **82.8%** (scheduler/worker/model-router 100%, rpc 82%, session-store 88%). Rust **90.6%** overall — pi-policy 96%, pi-patch 100%, pi-audit 95%, all above the §15 targets. Run `scripts/coverage.sh`.

**Acceptance gates:** `scripts/ci-gates.sh` proves the red lines on every run — no TypeScript runtime, core commands run with Node off `PATH`, patch/search fail without the Zig tools (no fallback), read-only + destructive commands are blocked. `scripts/bench.sh` checks the §14 performance targets (scan 10k files ~250ms, grep ~320ms, patch apply ~33ms).

**Auditability:** every event is chained with `prev_hash`/`event_hash` (SHA-256) and tagged with its language `actor` (go/rust/zig/model/user); `pi audit verify` detects any tampering. Patch writes and pty/command execution run in the Zig toolchain; the Rust kernel is the only path to a side effect.

| Phase | Goal | Status |
|-------|------|--------|
| 0 | Prove the three-language boundary (Go/Rust/Zig + JSON-RPC) | ✅ done |
| 1 | MVP: local coding-agent runtime (session, event log, patch, safe-edit) | ✅ done |
| 2 | Security kernel hardening (policy engine, secret broker, sandbox) | ✅ done |
| 3 | Daemon / worker / remote execution | ✅ done |
| 4 | WASM plugin ecosystem | ✅ done |
| 5 | Enterprise hardening | ✅ done |

The cross-language loop is wired: the Go daemon spawns `pi-kernel-service`
(Rust) over stdio JSON-RPC and shells out to the Zig tools. `go test
./go/daemon` runs a full end-to-end test — session → out-of-workspace read
denied → `pi-grep` search → patch apply → rollback → command allow/deny →
audit report — across all three languages.

## Upstream

The original TypeScript agent surface lives in the fork [TsekaLuk/pi](https://github.com/TsekaLuk/pi) (upstream: [earendil-works/pi](https://github.com/earendil-works/pi), MIT). Per the PRD, the Agent Surface may remain TypeScript and evolve independently.

## License

MIT
