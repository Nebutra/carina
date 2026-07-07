<div align="center">

<img src="docs/assets/carina-hero.png" alt="Nebutra Carina" width="100%" />

# Nebutra Carina

**Run coding agents on real repositories with policy, audit, and rollback in the loop.**

[![status](https://img.shields.io/badge/status-alpha-0033FE)](#current-status)
[![build](https://img.shields.io/badge/build-source%20first-0B7285)](#quickstart-from-source)
[![runtime](https://img.shields.io/badge/runtime-local--first-0BF1C3)](#why-carina)
[![audit](https://img.shields.io/badge/audit-hash--chained-6D28D9)](#review-and-audit)
[![license](https://img.shields.io/badge/license-Apache--2.0-informational)](LICENSE)

**English** · [简体中文](README.zh-CN.md) · [日本語](README.ja.md)

</div>

Carina is a local-first runtime layer for AI coding agents. It is not an
editor, a chat app, or a hosted sandbox. It sits between an agent and the
machine, so file reads, edits, commands, network access, plugins, and secrets go
through explicit policy before they happen.

The current repository is useful for source builds, local experimentation, and
teams designing their own agent execution substrate. It is still alpha:
packaged releases, public installer flows, and polished dashboards are not done
yet.

## Why Carina

Use Carina when the hard part is not asking a model for code, but controlling
what happens after the model decides to act.

Carina gives you:

- **Per-action permission decisions** for files, commands, network, secrets,
  patch application, plugins, and remote work.
- **Auditable execution** through an append-only hash chain that records
  decisions and granted side effects.
- **Transactional file changes** that can be proposed, inspected, applied, and
  rolled back.
- **Daemon-backed sessions** that can survive CLI exit and support background or
  remote workers.
- **BYOK model access** with provider catalog discovery and Nebutra OAuth as a
  fallback path when configured.
- **MCP, plugins, sub-agents, workflows, and egress controls** behind the same
  capability boundary.

## Good Fits

Carina is a good fit when you need to:

- run coding-agent tasks on local repositories without giving the agent raw
  machine access;
- keep a record of what the agent read, changed, ran, and why it was allowed;
- build an IDE extension, CI integration, internal agent platform, or workflow
  runner on top of a reusable runtime;
- let sub-agents, plugins, or remote workers operate with narrower permissions
  than the parent task;
- evaluate agent work in environments where rollback and audit matter.

Carina is not the right tool if you only need an editor assistant, a hosted
managed agent service, or a stable packaged release today.

## Current Status

Implemented in this repository:

| Area | What exists today |
|---|---|
| Sessions and tasks | Daemon-backed sessions, background runs, event streams, attach/replay, task steering |
| Agent loop | ReAct-style loop, structured actions, prompt compaction, success checks, verifier, risk review |
| Permissions | Built-in profiles, approval modes, approval overlays with justifications, workspace trust, sub-agent attenuation |
| Audit | Hash-chained event log, audit export, verification, normalized `session.items` stream, turn net diff |
| File changes | Transactional patch propose/apply/rollback and post-edit diagnostics |
| Commands | Risk classification, approval gates, command output events, optional OS sandbox backend |
| Network and secrets | Deny-by-default egress proxy, allowlists, daemon-side credential injection, explicit per-host HTTPS MITM opt-in |
| Models | BYOK auth chain, provider catalog, OpenAI/Anthropic/Gemini/OpenRouter-style runtime adapters |
| Integration | MCP client/server, WASM plugin boundary, workers, workflow DAGs |

Not yet treated as product-complete:

- signed public releases, Homebrew tap, and npm install channel;
- contributor and security process beyond the initial documents in this repo;
- polished TUI/dashboard;
- Windows support;
- SDK parity across TypeScript, Python, and Go;
- production guide for clustered remote-worker fleets.

## Quickstart From Source

Requirements:

- Go 1.25 or newer
- Rust 1.85 or newer
- Zig 0.15.x
- macOS or Linux

Build everything:

```bash
git clone https://github.com/Nebutra/carina
cd carina
make all
```

Start the daemon:

```bash
./bin/carina-daemon &
```

Provide a model credential to the daemon process. BYOK API keys have priority;
Nebutra OAuth fallback is supported when configured.

```bash
export ANTHROPIC_API_KEY=sk-...
# or
export OPENAI_API_KEY=sk-...
```

Run a task in the current repository:

```bash
./bin/carina run "fix the failing tests and show the patch"
```

Inspect what happened:

```bash
./bin/carina sessions
./bin/carina items <session_id>
./bin/carina audit verify <session_id>
./bin/carina patch list <session_id>
./bin/carina patch show <session_id> <patch_id>
```

Roll back an applied patch:

```bash
./bin/carina patch rollback <session_id> <patch_id>
```

## Common Workflows

### Local Repository Work

Use the default `safe-edit` session for normal development. The agent can read
the workspace, propose patches, and run allowlisted build/test commands. Risky
commands, network access, secrets, and plugins stay denied or approval-gated by
the active profile.

### Review And Audit

Use `carina items <session_id>` for a normalized thread/turn/item view, including
turn-level patch summaries. Use `carina audit <session_id>` or
`carina audit verify <session_id>` when you need the raw event chain and
tamper-evidence.

### BYOK Providers

Store local credentials and inspect the provider catalog:

```bash
./bin/carina auth login anthropic - < ~/.secrets/anthropic-key
./bin/carina auth login openai - < ~/.secrets/openai-key
./bin/carina auth list
./bin/carina providers list --refresh
```

Pick a runtime model explicitly when needed:

```bash
CARINA_REASONER_MODEL=openai/gpt-5 ./bin/carina-daemon &
./bin/carina run --model openrouter/anthropic/claude-sonnet-4-5 "inspect this migration"
```

### Agent Modes And Slash Commands

Discover reusable agents and commands at runtime:

```bash
./bin/carina agents list
./bin/carina commands list
./bin/carina run --agent plan "inspect the release risk"
./bin/carina run "/review main"
```

Built-ins include `build`, `plan`, `general`, and `explore`. User and project
overrides live under `~/.carina/agents`, `<repo>/.carina/agents`,
`~/.carina/commands`, and `<repo>/.carina/commands`.

### Embedding

Use JSON-RPC, SDKs, or MCP server mode when Carina should sit behind another UI:
an IDE extension, web console, CI workflow, or internal agent platform.

## How It Compares

This is a positioning map, not a winner/loser checklist. These projects optimize
for different jobs, and their capabilities change quickly. Use each project's
official docs as the source of truth.

| If you primarily need... | Common choices | Where Carina fits |
|---|---|---|
| In-editor coding assistance | Cursor, Windsurf, Cline, IDE extensions | Carina can back an editor, but it is not an editor product. |
| Terminal-first pair programming | Claude Code, Codex CLI, Aider, OpenCode | Carina focuses less on chat UX and more on runtime boundaries, audit, rollback, workers, and embeddability. |
| Cloud-hosted agent tasks | OpenAI Codex cloud tasks and managed agent services | Carina is local-first. Cloud identity and multi-endpoint sync should live behind Nebutra boundaries, not inside the local runtime. |
| Disposable cloud sandboxes | E2B and other sandbox runtimes | Carina can use sandboxing, but its core unit is policy-gated action on a repository, not a hosted VM product. |
| Internal agent infrastructure | Custom stacks, CI systems, internal platforms | Carina is meant to be used as a control-plane/runtime component. |

## Architecture

Carina is split by responsibility:

| Layer | Responsibility |
|---|---|
| Agent surface | Agent loop, transcripts, approvals, sub-agents, workflows |
| Control plane | Sessions, scheduling, JSON-RPC, workers, event streaming, egress |
| Capability kernel | Permission decisions, policies, transactional patches, audit chain, plugins |
| Native toolchain | Repository scan, grep, diff, patch, process execution, pty |
| Client surfaces | CLI, TUI, SDKs, MCP client/server |

The important boundary is not the language split. The important boundary is that
the agent requests actions, while the runtime decides whether they can happen
and records the result.

## Security Model

Default posture:

1. Least privilege by default.
2. No access outside the workspace unless explicitly granted.
3. Secrets are unreadable by default.
4. Network access is restricted by default.
5. Destructive commands are denied by default.
6. File changes go through patch transactions.
7. Plugins start with no implicit permissions.

Alpha limitations:

- Carina is not a VM or complete container isolation system by itself.
- OS sandbox backends exist, but production profiles need deployment review.
- Policy correctness depends on routing commands through the Carina daemon and
  toolchain.
- Public release signing and supply-chain provenance are not complete yet.

See [SECURITY.md](SECURITY.md) and [docs/security-model.md](docs/security-model.md).

## Development

Build and test:

```bash
make all
go test ./go/... ./apps/...
cargo test
go test -race ./go/daemon ./go/config ./apps/carina-daemon
```

Run the local release gate:

```bash
make release-check
```

More documentation:

- [Product positioning](docs/product.md)
- [Roadmap](docs/roadmap.md)
- [Release process](docs/release.md)
- [Architecture](docs/architecture.md)
- [RPC API](docs/rpc-api.md)
- [Plugin model](docs/plugin-model.md)
- [Research status](docs/research/absorption-status.md)

## License

Apache-2.0. See [LICENSE](LICENSE).
