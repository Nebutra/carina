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

Phase 0 — technology validation. See [docs/PRD.md](docs/PRD.md) for the full product requirements and [docs/architecture.md](docs/architecture.md) for the layered design.

| Phase | Goal | Status |
|-------|------|--------|
| 0 | Prove the three-language boundary (Go/Rust/Zig + JSON-RPC) | 🚧 in progress |
| 1 | MVP: local coding-agent runtime (session, event log, patch, safe-edit) | ⏳ |
| 2 | Security kernel hardening (policy engine, secret broker, sandbox) | ⏳ |
| 3 | Daemon / worker / remote execution | ⏳ |
| 4 | WASM plugin ecosystem | ⏳ |
| 5 | Enterprise hardening | ⏳ |

## Upstream

The original TypeScript agent surface lives in the fork [TsekaLuk/pi](https://github.com/TsekaLuk/pi) (upstream: [earendil-works/pi](https://github.com/earendil-works/pi), MIT). Per the PRD, the Agent Surface may remain TypeScript and evolve independently.

## License

MIT
