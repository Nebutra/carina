<div align="center">

<img src="docs/assets/carina-hero.png" alt="Nebutra Carina — the secure keel your agents run on" width="100%" />

# Nebutra Carina

**A secure agent runtime, written in Go, Rust & Zig.**

*The secure keel your agents run on — every side effect gated, audited, and reversible.*

[![build](https://img.shields.io/badge/build-passing-0033FE)](#)
[![release](https://img.shields.io/badge/release-v0.1.0--alpha-0BF1C3)](#)
[![stack](https://img.shields.io/badge/Go%20%C2%B7%20Rust%20%C2%B7%20Zig-polyglot-8b5cf6)](#)
[![license](https://img.shields.io/badge/license-Apache--2.0-informational)](#)
[![signed releases](https://img.shields.io/badge/releases-signed-0033FE)](#)
[![no telemetry](https://img.shields.io/badge/telemetry-none-0BF1C3)](#)
[![powered by Nebutra](https://img.shields.io/badge/powered%20by-Nebutra-0033FE)](#)

`curl -fsSL https://get.nebutra.com/carina | sh`

**English** · [简体中文](README.zh-CN.md) · [日本語](README.ja.md)

</div>

---

## What is Carina

**Carina is the secure substrate that _runs_ AI coding agents** — not an OS, not a framework, a runtime. You point an agent at a task; Carina executes the agent's ReAct loop while its **capability kernel gates every side effect at the boundary**, records each one in a **tamper-evident, hash-chained audit log**, and applies file changes as **transactional patches you can roll back**.

Everybody else sells an agent that _acts_. Carina runs an agent that acts **and can be trusted and undone**.

It's for the person who wants to give an agent real access to a real machine — write files, run commands, call tools — without handing over the keys and hoping. If you've ever wanted to answer *"what exactly did the agent do, and can I take it back?"* with a cryptographic yes, Carina is the runtime you've been stitching together out of a CLI, a sandbox, and an audit shim by hand.

One binary replaces that pile. You only need `carina`.

```
Go makes it run.  Rust makes it safe.  Zig makes it sharp.  LLM makes it useful.
```

---

## Why three languages (and an LLM)

This is the whole thesis. Each language has exactly **one job**, chosen for what it is uniquely good at. No resume-driven design — every dependency is earned.

| Layer | Language | The one job | Concrete mechanism | Measurable effect |
|---|---|---|---|---|
| **Control plane** | **Go** | *make it run* | Daemon, scheduler, session store, JSON-RPC surface, model router. Goroutine-per-session concurrency, one long-lived process. | Many agent sessions, remotely schedulable, on one supervised daemon. The CLI is a **client** — kill it and sessions survive. |
| **Capability kernel** | **Rust** | *make it safe* | Policy engine + rollbackable patch engine + hash-chained append-only audit log + plugin runtime. Every effect crosses a typed capability boundary. | **100% of side effects gated + audited.** Every patch **atomic and reversible.** Log is **tamper-evident** — alter one entry and the chain breaks. |
| **Native toolchain** | **Zig** | *make it sharp* | `scan`, `grep`, `diff`, `patch`, `pty` as tiny native binaries. No GC, no runtime, structured JSON output. | Fast, allocation-lean primitives that start in ~ms. The hot path an agent hits thousands of times isn't paying for a language runtime. |
| **Agent surface** | **LLM** | *make it useful* | ReAct loop with a typed transcript, compaction + loop-guard, sub-agents with **capability attenuation (child ⊆ parent)**, Codex-style goal / success-criteria + approval modes. | The agent can *think and act* — and a spawned sub-agent can **never exceed its parent's permissions.** Delegation without privilege escalation. |

### The mechanisms, in plain terms

- **Capability gating.** Agents never touch the OS directly. Reading a file, writing a patch, spawning a process, hitting the network — each is a **capability** the kernel must grant. Ungranted effect ⇒ the effect never happens. This is the difference between "the model promised not to `rm -rf`" and "the model was never handed the capability to."
- **Tamper-evident audit log.** Every granted effect is appended to a log where each entry embeds the hash of the previous one (`hash(N) = H(entry_N ‖ hash(N-1))`). The chain is verifiable end to end: you can prove nothing was inserted, deleted, or edited after the fact. Not "trust me," but "check the math."
- **Transactional, rollbackable patches.** File mutations don't stream to disk hoping for the best. The patch engine stages a change as a transaction, lets you preview it, applies it atomically, and keeps enough to **undo it cleanly**. A bad agent edit is one `carina rollback` away.
- **Sub-agent attenuation.** When an agent delegates, the child inherits a **subset** of the parent's capabilities — never a superset. A research sub-agent that only needed read access can't suddenly write. Least privilege, enforced structurally, all the way down the tree.
- **Native Zig tools.** The primitives agents lean on constantly (grep a repo, diff a change, drive a pty) are native and structured — designed to be called by a machine, emitting JSON, not scraped from human-formatted stdout.

> **Mission.** The next decade of software will be written with agents in the loop. Carina is a bet that "agent with real access" and "under control" are not a trade-off — that safety, rendered correctly, is just good infrastructure.

---

## Architecture

```
┌───────────────────────────────────────────────────────────┐
│                      Agent Surface  (LLM)                  │
│   CLI · TUI · SDK · ReAct loop · sub-agents · approval     │
└───────────────────────────────┬───────────────────────────┘
                                │ JSON-RPC
┌───────────────────────────────▼───────────────────────────┐
│                   Control Plane  (Go)                      │
│   daemon · scheduler · session store · model router        │
│   "make it run"                                            │
└───────────────────────────────┬───────────────────────────┘
                                │ Capability API  (every effect crosses here)
┌───────────────────────────────▼───────────────────────────┐
│                 Capability Kernel  (Rust)                  │
│   policy engine · transactional patch · hash-chained audit │
│   plugin runtime · "make it safe"                          │
└───────────────────────────────┬───────────────────────────┘
                                │ Native tool calls
┌───────────────────────────────▼───────────────────────────┐
│                 Native Toolchain  (Zig)                    │
│   scan · grep · diff · patch · pty · "make it sharp"       │
└───────────────────────────────────────────────────────────┘
```

**Core invariants**

1. Agents never touch system resources directly.
2. Every side effect passes through the capability kernel.
3. Every granted effect is appended to the hash-chained audit log.
4. Every patch is previewable, verifiable, and rollbackable.
5. Every tool and plugin declares its capabilities explicitly.
6. Local-first by default; remote execution is an extension, not a requirement.
7. The CLI is a client — the daemon is the runtime.

Depth lives in [`docs/architecture.md`](docs/architecture.md) and [`docs/security-model.md`](docs/security-model.md) — this README discloses the model, not the internals.

---

## Install

> **Requires:** macOS or Linux (x86-64 / arm64). Windows is on the [roadmap](#roadmap).

**One command (recommended):**

```bash
curl -fsSL https://get.nebutra.com/carina | sh
```

**Homebrew:**

```bash
brew install nebutra/tap/carina
```

**From source** — needs Go ≥ 1.25, Rust ≥ 1.85, Zig 0.15.x:

```bash
git clone https://github.com/Nebutra/carina && cd carina
make all        # builds Go control plane, Rust kernel crates, Zig tools
```

Verify:

```bash
carina --version   # carina 0.1.0-alpha
```

---

## Quickstart — your first agent run

```bash
# 1. Start the runtime (control-plane daemon; sessions outlive your shell)
carina daemon &
#   ⇒ carina daemon listening on ~/.carina/daemon.sock

# 2. Point an API key at it (never hardcoded — env only)
export ANTHROPIC_API_KEY=sk-...

# 3. Run your first agent against a task
carina run "add a --json flag to the status command and update its test"
#   ⇒ session f3a9c1  created
#   ⇒ [react] plan → grep → edit → test
#   ⇒ [gate]  write  cmd/status.go              approved (capability: fs.write)
#   ⇒ [patch] staged 1 file · atomic · rollbackable  →  cr patch show f3a9c1
#   ⇒ [audit] entry 0007 chained  sha256:9b1e…  (prev 3c7a…)
#   ⇒ done · success criteria met · 1 patch applied

# 4. Inspect exactly what happened — and verify the chain
carina audit f3a9c1 --verify
#   ⇒ 7 entries · chain intact · no tampering detected ✓

# 5. Don't like it? Take it back, atomically.
carina rollback f3a9c1
#   ⇒ reverted 1 patch · workspace clean
```

Short alias `cr` is available for every verb (`cr run`, `cr audit`). Prefer the full `carina` in scripts and docs.

---

## Key capabilities

- 🔒 **Capability gating** — every side effect requires an explicit grant; ungranted ⇒ never happens.
- 🧾 **Hash-chained audit log** — append-only, tamper-evident, verifiable end to end.
- ↩️ **Transactional rollback** — patches are atomic and cleanly reversible.
- 🧬 **Sub-agent attenuation** — child capabilities ⊆ parent, enforced structurally.
- 🔁 **ReAct loop + loop-guard** — typed transcript, compaction, runaway detection.
- ✋ **Approval modes** — Codex-style goal / success-criteria; require human sign-off on chosen effect classes.
- ⚡ **Native Zig toolchain** — scan / grep / diff / patch / pty, structured JSON, no GC.
- 🛰️ **Daemon-first** — sessions are remotely schedulable and survive the CLI.
- 🔌 **Sandboxed plugin runtime** — third-party tools run under the same capability contract.
- 🙈 **No telemetry** — nothing phones home; the log is yours.

---

## Security & auditability

For an agent runtime, **security is the product.** Carina's guarantee is a chain of three properties, each verifiable:

1. **Gated** — the capability kernel is the *only* path to a side effect. There is no back door where the model calls `exec` directly; it asks the kernel, and the kernel decides against policy. Approval modes let you interpose a human on any effect class (writes, network, process spawn).
2. **Audited** — every granted effect becomes an entry in the append-only log, each entry hash-chained to the last. `carina audit <session> --verify` recomputes the chain and reports tampering. Because the linkage is cryptographic, an attacker who edits history has to break every subsequent hash — and can't.
3. **Reversible** — the patch engine treats mutations as transactions. Preview before, roll back after, atomically. "The agent broke my repo" stops being a disaster and becomes a `rollback`.

Sub-agents inherit **attenuated** capability sets (child ⊆ parent), so delegation can never escalate privilege. Plugins run in the same sandbox under the same explicit-capability contract as first-party tools.

Threat model, log format, and verification protocol: [`docs/security-model.md`](docs/security-model.md).

**Reporting a vulnerability:** email **security@nebutra.com** (see [`SECURITY.md`](SECURITY.md)). Please don't open public issues for security reports.

---

## Prior art & lineage

Carina stands on known-good techniques, not novelty for its own sake: the **ReAct** loop (reason + act), **Codex-style** goal / success-criteria and approval modes, **capability-based security** with attenuation (child ⊆ parent), and tamper-evident **hash-chained logs**. The contribution is putting them under one runtime with one enforcement boundary.

---

## How it compares

| | Carina | Aider / Cline | Cursor / Windsurf | E2B / sandboxes |
|---|---|---|---|---|
| Runs agents on a supervised daemon | ✅ | ❌ (CLI-bound) | ❌ (editor-bound) | partial |
| Every side effect capability-gated | ✅ | ❌ | ❌ | ✅ (isolation, not per-effect) |
| Tamper-evident audit log | ✅ | ❌ | ❌ | ❌ |
| Transactional rollback of edits | ✅ | git-only | git-only | ❌ |
| Sub-agent capability attenuation | ✅ | ❌ | ❌ | ❌ |

Carina is **not** an editor, and **not** a hosted product. It's the substrate the others could run on.

---

## Roadmap

Honest state of things. Carina is **alpha** — the enforcement core is real; the ecosystem around it is early.

**Shipped**
- [x] Go control plane: daemon, scheduler, session store, JSON-RPC
- [x] Rust capability kernel: policy engine + capability gating
- [x] Tamper-evident hash-chained audit log + `--verify`
- [x] Transactional, rollbackable patch engine
- [x] Zig native toolchain: scan / grep / diff / patch / pty
- [x] ReAct agent loop with typed transcript, compaction, loop-guard
- [x] Sub-agent capability attenuation (child ⊆ parent)
- [x] `carina` CLI (alias `cr`)

**Planned**
- [ ] Workflow orchestration engine (multi-step, resumable agent pipelines)
- [ ] Additional model providers beyond the initial router set
- [ ] Sandbox profiles (per-project capability presets & templates)
- [ ] Plugin marketplace + signed plugin distribution
- [ ] TUI dashboard for live session + audit-stream inspection
- [ ] Remote / clustered execution across worker nodes
- [ ] Windows support
- [ ] SDK parity across TypeScript / Python / Go
- [ ] SLSA build provenance on all release artifacts

Track it in [GitHub Issues](https://github.com/Nebutra/carina/issues) — gaps are contribution opportunities, not surprises.

---

## Contributing

Per-OS dev-build guides and the architecture tour live in [`CONTRIBUTING.md`](CONTRIBUTING.md). The build is `make go` / `make rust` / `make zig` / `make all`. PRs that add a capability must also add its audit-log coverage — that's the house rule.

## Community

- Discussions: [GitHub Discussions](https://github.com/Nebutra/carina/discussions)
- Chat: [Discord](https://discord.gg/nebutra)
- Updates: [@nebutra](https://x.com/nebutra)

## License

Apache-2.0 — see [`LICENSE`](LICENSE).

<div align="center">

**Nebutra Carina** — the secure keel your agents run on.
Powered by [Nebutra](https://nebutra.com) · sibling to [Sailor](https://github.com/Nebutra/create-sailor).

</div>