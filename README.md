<div align="center">

<img src="docs/assets/carina-hero.png" alt="Nebutra Carina" width="100%" />

# Nebutra Carina

**A local-first runtime for running coding agents behind explicit permission,
audit, and rollback boundaries.**

[![status](https://img.shields.io/badge/status-alpha-0033FE)](#current-repository-state)
[![build](https://img.shields.io/badge/build-source%20first-0B7285)](#quickstart-from-source)
[![runtime](https://img.shields.io/badge/runtime-local--first-0BF1C3)](#what-carina-is)
[![audit](https://img.shields.io/badge/audit-hash--chained-6D28D9)](#core-concepts)
[![license](https://img.shields.io/badge/license-Apache--2.0-informational)](LICENSE)

**English** · [简体中文](README.zh-CN.md) · [日本語](README.ja.md)

</div>

Status: **alpha**. The enforcement core is implemented in this repository, but
packaging, public release infrastructure, and some UX surfaces are still early.
Expect CLI details and configuration formats to change.

---

## What Carina Is

Carina is not an editor, a chat product, or a hosted sandbox. It is the runtime
layer that sits between an AI coding agent and the machine it wants to act on.

When an agent needs to read files, propose edits, run commands, access the
network, call plugins, or use secrets, Carina routes those actions through a
capability kernel. The kernel decides whether the action is allowed, denied, or
requires approval. Allowed effects are recorded in a hash-chained audit log, and
file changes are applied as transactional patches that can be inspected and
rolled back.

The goal is simple: let agents do useful work on real repositories without
turning every tool call into implicit, untracked machine access.

## When To Use It

Carina is designed for cases where a coding agent needs real execution access
and you care about control after the prompt is sent.

Good fits:

- Running agent tasks on local repositories while keeping writes, commands,
  network access, and secrets behind policy.
- Keeping long-running or background agent sessions alive after the CLI exits.
- Producing an event stream that can answer what the agent did, when it did it,
  and why it was allowed.
- Rolling back agent-produced file changes without depending only on ad hoc Git
  cleanup.
- Building an IDE, CI integration, internal agent platform, or workflow engine
  that needs a reusable execution substrate.
- Letting sub-agents or plugins work with narrower permissions than the parent
  task.

Poor fits:

- You only want an editor assistant or chat UI.
- You want a hosted, managed agent service.
- You do not need audit logs, policy boundaries, rollback, or daemon-backed
  sessions.
- You need a stable packaged release today. Source builds are the most reliable
  path at this stage.

## Current Repository State

Implemented in the current codebase:

- Go daemon and CLI client for sessions, tasks, scheduling, JSON-RPC, model
  routing, workers, and event streaming.
- Rust capability kernel for permission decisions, policy enforcement,
  transactional patches, audit logs, and plugin execution boundaries.
- Zig native tools for scan, grep, diff, patch, command execution, and pty
  primitives.
- Built-in permission profiles such as `read-only`, `safe-edit`,
  `full-workspace`, `ci-runner`, and enterprise-oriented profiles.
- Hash-chained append-only audit logs with verification.
- Transactional patch proposal, apply, and rollback flow.
- ReAct-style agent loop with typed transcript, compaction, loop guards,
  completion verification, and background run recovery.
- Sub-agents with capability attenuation: a child session can receive a subset
  of the parent's permissions, not a superset.
- Workflow orchestration for declarative DAGs of agent steps.
- MCP client and server interop, routed through the same capability boundary.
- Deny-by-default egress proxy, allowlisted network access, daemon-side
  credential injection, and explicit per-host HTTPS MITM opt-in for credential
  injection.
- Secret handling through brokered access rather than directly exposing process
  environment values to agent commands.

Not yet treated as done:

- Public installer and Homebrew tap.
- Published `SECURITY.md` and contributor guide.
- Stable release artifacts with provenance.
- Polished TUI/dashboard experience.
- Windows support.
- SDK parity across TypeScript, Python, and Go.
- Production documentation for operating clustered or remote worker fleets.

## Quickstart From Source

Requirements:

- Go 1.25 or newer
- Rust 1.85 or newer
- Zig 0.15.x
- macOS or Linux

Build:

```bash
git clone https://github.com/Nebutra/carina
cd carina
make all
```

Start the daemon:

```bash
./bin/carina-daemon &
```

Provide a model credential to the daemon process environment. BYOK API keys take
priority; Nebutra OAuth fallback is supported by the daemon when configured.

```bash
export ANTHROPIC_API_KEY=sk-...
# or
export OPENAI_API_KEY=sk-...
```

You can also store a local BYOK credential and inspect the provider catalog:

```bash
./bin/carina auth login anthropic - < ~/.secrets/anthropic-key
./bin/carina auth login openai - < ~/.secrets/openai-key
./bin/carina auth list
./bin/carina providers list --refresh
```

The daemon reads the cached catalog at startup. Set
`CARINA_PROVIDER_REFRESH=1` when starting the daemon if you want it to refresh
models.dev first. Runtime adapters currently cover Anthropic Messages, OpenAI
Responses, OpenAI-compatible chat providers from the catalog, OpenRouter, and
Google Gemini. Cloud identity providers such as Bedrock, Azure OpenAI, and
Vertex need separate region/project credential wiring.

Pick a runtime model explicitly when needed:

```bash
CARINA_REASONER_MODEL=openai/gpt-5 ./bin/carina-daemon &
./bin/carina run --model openrouter/anthropic/claude-sonnet-4-5 "fix the failing tests"
```

Run a task in the current repository:

```bash
./bin/carina run "fix the failing tests and show the patch"
```

Inspect the session:

```bash
./bin/carina sessions
./bin/carina audit <session_id>
./bin/carina audit verify <session_id>
./bin/carina patch list <session_id>
./bin/carina patch show <session_id> <patch_id>
```

Rollback an applied patch:

```bash
./bin/carina patch rollback <session_id> <patch_id>
```

## Common Workflows

### Personal repository work

Use `safe-edit` or a stricter profile for normal coding tasks. The agent can
read the workspace, propose patches, and run allowed test/build commands. Risky
commands, network access, and secrets stay denied or approval-gated depending on
the active profile.

### Team or security review

Use the audit stream and audit export to inspect which files were read, which
commands ran, which permission decisions were made, and which patch changed a
file. The hash chain lets a verifier detect edits to the recorded event history.

### Background or remote work

The CLI is a client; the daemon owns runtime state. Sessions and background runs
can survive CLI exit, and the worker interfaces are designed for local,
remote, CI, or sandboxed execution pools.

### Runtime embedding

Use the JSON-RPC server, SDKs, or MCP server mode when Carina should sit behind
another product surface: an IDE extension, a web UI, a CI workflow, or an
internal agent platform.

## Core Concepts

### Capability boundary

Carina represents side effects as capabilities such as file read/write, command
execution, network access, secret access, patch application, plugin loading, and
remote execution. A session's permission profile controls which requests are
allowed, denied, or sent for approval.

### Audit log

Every permission decision and granted side effect is recorded as an event. Events
are appended to a hash chain: each event includes the previous event hash. A
verification pass can detect inserted, removed, or edited events.

### Transactional patches

Agent file changes are represented as patch transactions. A patch can be
proposed, inspected, applied, and rolled back. The patch system is intended to
avoid half-applied file mutations and to preserve provenance for each change.

### Daemon-backed sessions

The daemon stores session state, schedules tasks, streams events, and coordinates
workers. This lets tasks continue outside the lifetime of a single terminal
process.

### Sub-agent attenuation

When a task spawns a sub-agent, the child receives an attenuated permission set.
The child can be less privileged than the parent, but it cannot gain permissions
the parent did not have.

### Egress and secrets

Network access is deny-by-default when the egress proxy is enabled. Hosts must be
allowed by policy. Credentials can be injected at the egress boundary from
daemon-side secrets, so command children do not need the raw secret in their
environment. HTTPS credential injection requires explicit per-host MITM opt-in
and uses a process-local trust bundle rather than modifying system trust.

## Architecture

Carina is split by responsibility, not by technology showcase.

| Layer | Main responsibility | Current implementation |
|---|---|---|
| Agent surface | ReAct loop, transcript, approvals, sub-agents, workflow execution | Go daemon plus model-router integrations |
| Control plane | Sessions, scheduling, JSON-RPC, workers, event streaming, egress proxy | Go |
| Capability kernel | Permission decisions, policies, transactional patches, audit chain, plugin boundary | Rust |
| Native toolchain | Repository scan, grep, diff, patch, process execution, pty | Zig |
| Client surfaces | CLI, TUI, SDKs, MCP server/client integration | Go plus SDK packages |

The design keeps the model-facing loop separate from the side-effect boundary.
The agent can request an action; the runtime decides whether the action can
happen and records the result.

More detail:

- [Architecture](docs/architecture.md)
- [Security model](docs/security-model.md)
- [RPC API](docs/rpc-api.md)
- [Plugin model](docs/plugin-model.md)
- [Enterprise notes](docs/enterprise.md)

## Security Model

Default posture:

1. Least privilege by default.
2. No access outside the workspace unless explicitly granted.
3. Secrets are unreadable by default.
4. Network access is restricted by default.
5. Destructive commands are denied by default.
6. File changes go through patch transactions.
7. Plugins start with no implicit permissions.

Built-in profiles define common policy bundles:

| Profile | Intended use |
|---|---|
| `read-only` | Inspect a workspace without writes, commands, network, or secrets. |
| `safe-edit` | Read workspace files, write through patches, run allowlisted test/build commands. |
| `full-workspace` | Broader workspace access, still audited and approval-aware. |
| `ci-runner` | Test/build automation with restricted shell and secret access. |
| `enterprise-restricted` | Organization policy overlays and central approval rules. |

Security boundaries are only useful if they are documented with limitations.
Important limits in alpha:

- Carina is not a VM or a full container isolation system by itself.
- OS-level sandboxing is implemented for selected backends, but deployment
  profiles need review before production use.
- Policy correctness depends on running commands through the Carina toolchain
  and daemon-controlled environment.
- Public packaging and supply-chain provenance are not complete yet.

## How Carina Relates To Other Tools

This is not a winner/loser feature checklist. The tools in this space optimize
for different jobs, and their capabilities change quickly. Use each project's
own documentation as the source of truth.

| If you primarily need... | Commonly used tools | Where Carina fits |
|---|---|---|
| In-editor code assistance and interactive UX | Cursor, Windsurf, Cline, IDE extensions | Carina is lower-level. It can back an editor surface but is not trying to replace one. |
| Pair-programming from a CLI | Aider, Claude Code style CLIs, Codex-style CLIs | Carina focuses on the runtime boundary: daemon sessions, policy, audit, rollback, workers. |
| Disposable hosted execution environments | E2B and other sandbox providers | Carina is local-first runtime infrastructure. It can use sandboxing, but its core concern is per-action control and provenance. |
| A reusable execution substrate for internal agents | Custom agent stacks, CI systems, internal platforms | Carina is intended to be embedded behind other UIs and workflows. |

The practical distinction: Carina puts less emphasis on front-end polish and
more emphasis on making agent execution inspectable, policy-controlled, and
reversible.

## Roadmap

Near-term priorities:

- Publish installation paths: signed releases, hosted installer, Homebrew tap,
  and supply-chain provenance.
- Add `SECURITY.md`, contributor documentation, and release-process docs.
- Improve TUI and live audit inspection.
- Harden remote worker operation and document production deployment patterns.
- Improve SDK parity across TypeScript, Python, and Go.
- Continue expanding policy profiles, sandbox backends, and plugin signing.
- Add Windows support after the core Unix path is stable.

## Development

Build everything:

```bash
make all
```

Run Go tests:

```bash
make go-test
```

Run Rust tests:

```bash
make rust-test
```

Build Zig tools:

```bash
make zig
```

Useful documentation:

- [PRD](docs/PRD.md)
- [Agent model](docs/agent.md)
- [Architecture](docs/architecture.md)
- [Security model](docs/security-model.md)
- [Research status](docs/research/absorption-status.md)

## License

Apache-2.0. See [LICENSE](LICENSE).
