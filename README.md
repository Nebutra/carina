<div align="center">

<img src="docs/brand/assets/hero/carina-readme-hero.webp" alt="Nebutra Carina symbol on a dark mineral surface" width="100%" />

# Nebutra Carina

**Run coding agents on real repositories with policy, audit, and rollback in the loop.**

[![status](https://img.shields.io/badge/status-alpha-8E4053)](#current-status)
[![build](https://img.shields.io/badge/build-source%20first-176F70)](#quickstart-from-source)
[![runtime](https://img.shields.io/badge/runtime-local--first-087C58)](#why-carina)
[![audit](https://img.shields.io/badge/audit-hash--chained-8C5A15)](#review-and-audit)
[![license](https://img.shields.io/badge/license-MIT-182023)](LICENSE)

**English** · [简体中文](README.zh-CN.md) · [日本語](README.ja.md)

</div>

Carina is a local-first runtime layer for AI coding agents. It is not an
editor, a chat app, or a hosted sandbox. It sits between an agent and the
machine, so file reads, edits, commands, network access, plugins, and secrets go
through explicit policy before they happen.

The current repository is useful for source builds, local experimentation, and
teams designing their own agent execution substrate. It is still alpha. Public
macOS packages are available through the Nebutra Homebrew tap. Apple signing
and notarization automation is implemented but awaits release credentials.
Linux archives/packages, npm trusted publishing, Windows worker, containers,
and packaged VS Code/Web Operator clients are implemented in the release
pipeline but still need their external registries, publishers, or credentials
activated.

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
| Agent loop | ReAct-style loop, structured actions, dual-threshold/token-triggered prompt compaction with verbatim-user preservation, structured compaction summaries, canonical-signature loop detection, consecutive-failure circuit breaker, opt-in best-of-N patch generation, success checks, verifier, risk review |
| Memory | Local governed memory store with `memory` / Nebutra-scoped `user` targets, frozen per-run prompt snapshot, native `memory` tool, CLI/RPC inspection, and kernel-gated `MemoryWrite` audit |
| Permissions | Built-in profiles, approval modes, approval overlays with justifications, workspace trust, org-locked config keys, declarative sub-agent manifests with per-agent tool allow-lists and a kernel-gated spawn capability |
| Audit | Hash-chained event log, audit export, verification, normalized `session.items` stream, turn net diff |
| File changes | Transactional patch propose/apply/rollback and post-edit diagnostics |
| Commands | Risk classification, approval gates, command output events, optional OS sandbox backend |
| Network and secrets | Deny-by-default egress proxy, allowlists, daemon-side credential injection, explicit per-host HTTPS MITM opt-in |
| Models | BYOK auth chain, provider catalog, OpenAI/Anthropic/Gemini/OpenRouter-style runtime adapters, catalog-gated image input for vision-capable models (raw bytes stay in the artifact store, never in transcripts or audit) |
| Context engine | Native context-engine boundary with bundled/configured Headroom discovery, managed private MCP transport, and `carina context` diagnostics |
| Integration | MCP client/server with tool search (`mcp_find`), WASM plugin boundary with org/user/project tighten-only enable merge, workers, workflow DAGs (batch and streaming — conditional/dynamic graphs, live inter-step channels, remote worker-pool dispatch, run-wide budgets; see [`docs/workflows.md`](docs/workflows.md)) |
| Nebutra boundary | Local runtime stays authoritative; identity and multi-endpoint sync are scoped to Nebutra Cloud (`nebutra.com`) |

External activation still required:

- the first credentialed Apple-accepted public release; fail-closed signing and
  notarization automation is ready, but the required Apple credentials have not
  been provisioned;
- public Linux/npm/container publication; build, package, SBOM, provenance, and
  conformance paths are implemented, while external publisher/registry setup is
  not;
- Marketplace/hosting activation for the packaged VS Code and Web Operator
  clients;
- Homebrew Core review for untapped `brew install carina`; the maintained
  Nebutra tap is available now;
- real-provider/CJK/terminal validation requiring external credentials and
  representative hardware;
- Nebutra Cloud API, tenant, identity, and retention contracts. Local sync
  remains deliberately off;
- Windows is supported for the remote worker package, not a desktop daemon/CLI.

## Install With Homebrew

Carina publishes checksummed macOS packages for Apple Silicon and Intel through
the official Nebutra tap:

```bash
brew install Nebutra/tap/carina
```

The fully qualified command taps and trusts the Carina formula. After that,
`brew install carina` resolves the same formula.

Upgrade Carina with Homebrew's standard update flow:

```bash
brew update
brew upgrade carina
```

`brew update carina` is not a valid Homebrew command; `brew update` refreshes
package metadata and `brew upgrade carina` upgrades the installed formula.
Carina does not auto-start the daemon after installation.

## Built-in Updates

Check or install the newest public release from any installation:

```bash
carina update --check
carina update
```

Homebrew installations delegate to `brew`; npm/pnpm installations delegate to
their owning package manager. Standalone and source-tree `bin/carina`
installations download the complete platform bundle, verify the published
SHA256 plus the archive manifest and internal checksums, reject unsafe archive
entries, and replace sibling runtime binaries as one rollback-capable
transaction. Use `--version x.y.z` for an exact standalone release and
`--force` only for an intentional reinstall or downgrade. A running daemon is
never killed automatically; restart it after active tasks finish.

## TUI Interaction And Keybindings

Run `carina-tui` directly, or run bare `carina` in an interactive terminal. The
composer keeps control keys responsive while a task submission is waiting for
its daemon acknowledgement. Ordinary typing or paste during that interval
starts an independent next draft; it cannot mutate the frozen, journaled
submission being reconciled.

The default interaction loop is:

- `Enter` submits while idle and steers the active task; `Tab` queues a later
  turn and `Alt+Up` recalls the newest queued turn for editing.
- `Shift+Enter`, `Alt+Enter`, or `Ctrl+J` inserts a newline. `Ctrl+R` searches
  prompt history scoped to the current workspace, so another repository's
  prompts do not appear in normal TUI recall.
- `Esc` interrupts an active task. With an idle, empty composer, press `Esc`
  twice to open the checkpoint picker. Restore always requires a rollback
  preview, then `y` and `Enter` confirmation.
- `Alt+R` opens the plain-text transcript, `F1` opens context-aware help, and
  the mouse wheel scrolls the currently focused transcript, approval,
  question, help, keymap, or checkpoint surface.
- `!` on an empty composer enters **sticky shell mode** (prompt becomes `! `;
  Enter runs a governed `command.exec`). `Esc` on an empty shell draft returns
  to chat. One-shot `!cmd` in normal mode still works.
- `/settings` (or `Ctrl+,`) opens the control shell. `/plan` scaffolds a plan
  file under `.carina/plans/`; `/approve-plan` exits plan mode.
- **Product HITL modes** (footer; daemon config `approval_mode` /
  `CARINA_APPROVAL_MODE` / `-approval-mode` / `/approval-mode`):
  - `ask` — pause on `requires_approval` for operator once/session/project
  - `always-approve` — auto-allow `requires_approval` **with an on-screen
    warning** (`/always-approve`); deny rules, plan mode, and OS sandbox still apply
  - `dont-ask` — deny `requires_approval` unless a matching session/project
    grant already exists (exact resource, or a safe `FileRead`/`FileWrite`
    directory prefix; no prompt; CI-friendly). Also `/dont-ask`
  - `accept-edits` — auto-allow `FileWrite`/`PatchApply` `requires_approval`;
    shell/network/secrets still prompt (`/accept-edits`)
  - Orgs can lock out YOLO with `"disable_always_approve": true` (manage-lock
    in `/etc/carina/managed.json`)
  - **Not the same axis as session/kernel approval:** session create still takes
    `untrusted` \| `on_request` \| `never` (how the kernel escalates or
    auto-allows). Product mode is what the daemon does when the kernel still
    returns `requires_approval`. Do not set product `approval_mode` to `never`
    — that token is rejected so it cannot be confused with session `never`.
- `/plan` scaffolds `.carina/plans/`; **`/view-plan`** opens a plan review
  overlay (`a` approve, `s` request changes, `q` quit plan, `esc` close).
  `/approve-plan` still works from the command line / settings.
- `/btw <q>` is answer-only on the current session; `/btw --fork` / `/side`
  forks a session and switches to it (no dual-pane).
- Context pressure notices appear around 80%/90%; auto-compact runs only when
  a paused checkpoint makes `session.checkpoint.compact` available.
- Agent `ask_user`: **structured** (2–6 options) or **free-text** (omit
  `options`); free-text answers use the TUI free-text field.
- Autonomous **risk review** (always-approve / accept-edits edit path) is
  projected into the transcript with outcome / risk / rationale.

The TUI normally uses the alternate screen. To keep the rendered session in
the terminal's normal buffer, use `carina-tui --no-alt-screen`, or set
`"tui_alternate_screen": "never"` for both `carina-tui` and bare `carina`.
The accepted values are `auto`, `always`, and `never`.

The TUI ships authored copy for English, Simplified Chinese
(`zh-CN`/`zh-Hans`, runtime key `zh`), Traditional Chinese (`zh-Hant` /
`zh-TW` / `zh-HK` / `zh-MO`, runtime key `zh-Hant`), Japanese, Korean,
Spanish, and French. Traditional catalogs are derived from Simplified with
OpenCC-compatible conversion (regenerate via `scripts/gen_zh_hant.py`); the
Simplified `zh` strings remain the source of truth. Select locale with
`carina-tui --locale`, `CARINA_LOCALE`, `tui_locale` in the normal config
cascade, or `CARINA_TUI_LOCALE`; the precedence is flag, `CARINA_LOCALE`,
config, then `LC_ALL`/`LC_MESSAGES`/`LANG`. System detection for an
unsupported locale quietly falls back to English, while an unsupported
explicit flag, environment value, or config value fails fast instead of
silently choosing a different language.

The interactive TUI reads key overrides from the normal global and project
configuration cascade (`~/.carina/config.json` and `.carina/config.json`).
Bindings are keyed by semantic action names shown in the F1 help overlay and
the `/keymap` picker:

```json
{
  "tui_keybindings": {
    "global.help": ["f2"],
    "chat.interrupt": ["esc"],
    "composer.submit": ["ctrl+enter"],
    "composer.external-editor": ["ctrl+g"],
    "editor.move-word-left": ["alt+left", "alt+b"]
  }
}
```

Unknown actions, duplicate JSON keys, conflicting keys in the same context, and
unbound critical approval/search escape actions fail with an actionable error.
Cross-context bindings are checked against the contexts that can actually be
active together, including suggestions, normal transcript controls, and legacy
terminal-equivalent keys such as `Ctrl+[` and `Esc`. Printable pager bindings
remain overlay-only, so they cannot steal composer text. Modifier aliases such
as `control`, `option`/`opt`, `cmd`/`command`, and `win` normalize to the runtime
`ctrl`, `alt`, and `super` vocabulary.
Multi-key chords use a space-separated value such as `"ctrl+x ctrl+r"`.
Chord prefixes must start with a reliable modifier key; pending chords are
visible in the status line, time out after 1.2 seconds, and `Esc` cancels them.
Ambiguous single-key/chord prefixes are rejected instead of delaying or losing
ordinary input.
`/keymap` can replace a binding, add an alternate, or restore the inherited
value. During capture, `Ctrl+V` quotes the next key: for example, `Ctrl+V Esc`
records Escape, while `Ctrl+X Ctrl+V Enter` records a chord ending in Enter.
Bare Escape still cancels and bare Enter still saves, so capture always has a
reachable exit. The picker writes project `.carina/config.json` atomically and
applies the validated keymap immediately. External edits to managed, global, or
project config are hot-reloaded; invalid edits leave the last-good keymap
active.

Task submissions are journaled under the configured state directory before
dispatch. If an acknowledgement is lost or the TUI restarts, Carina reconciles
the same idempotency key instead of silently creating a duplicate task. Use the
`composer.submit-new` binding (default `Alt+S`) only when a distinct submission
is intentional.

## Quickstart From Source

Requirements:

- Go 1.25 or newer
- Rust 1.85 or newer
- Zig 0.15.x
- macOS or Linux

Build and install:

```bash
git clone https://github.com/Nebutra/carina
cd carina
make install
```

`make install` builds everything and installs the `carina*` binaries into
`~/.local/bin` (override with `PREFIX=/usr/local`); make sure that directory
is on `PATH`. To build without installing, run `make all` and use
`./bin/carina` directly. Homebrew installs already put `carina` on `PATH`.

Start the daemon:

```bash
carina-daemon &
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
carina run "fix the failing tests and show the patch"
```

The CLI prints a continuation hint after submission:

```bash
To continue this session, run:
  carina resume <session_id>
```

Inspect what happened:

```bash
carina sessions
carina resume <session_id> "follow up on the previous task"
carina items <session_id>
carina audit verify <session_id>
carina patch list <session_id>
carina patch show <session_id> <patch_id>
```

Roll back an applied patch:

```bash
carina patch rollback <session_id> <patch_id>
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

### Governed Memory

Carina keeps local long-term memory under the daemon state directory. The
runtime separates agent/project notes (`target=memory`) from user profile facts
(`target=user`). User memory scope follows Nebutra canonical identity when
`CARINA_NEBUTRA_IDENTITY_JSON` is present, then Nebutra OIDC/JWT claims from
`CARINA_NEBUTRA_TOKEN`, then a local fallback. These claims are scope metadata,
not local authorization grants. Memory enters an agent run as a frozen prompt
snapshot, so writes during that run are durable but do not rewrite the run's
stable prompt prefix. Use `carina memory ...`, the local `memory.*` RPC methods,
or the agent's native `memory` tool to add, replace, remove, or batch memory
entries. Writes go through the default approval-gated `MemoryWrite` capability,
are bounded and content-scanned, and are audited by target/scope/action/content
hash rather than by raw memory text.

`carina memory status <session_id>` reports local storage paths, identity
scope, cached external-provider health, HMS projection state, and Nebutra Cloud
sync status. HMS recall and governed projection are explicit, restart-only
opt-ins. Projection uses a durable desired-state outbox and requires
`NetworkAccess` plus `MemoryExternalize` independently of `MemoryWrite`;
Nebutra Cloud memory sync remains off.

### Native Context Engine

Release packages include a pinned Headroom executable as `bin/headroom`.
`context_engine=auto` enables Headroom only when it is bundled with Carina or
explicitly configured through `CARINA_HEADROOM_BIN`, `headroom_bin`, or
`--headroom-bin`. A global `headroom` found on `PATH` is reported but not used
as the built-in engine.

Inspect the integration:

```bash
carina context status
carina context doctor
carina context stats
```

The managed Headroom MCP server is private to Carina's context adapter. It does
not appear in the agent's public MCP tool list.

### BYOK Providers

Store local credentials and inspect the provider catalog:

```bash
carina auth login anthropic - < ~/.secrets/anthropic-key
carina auth login openai - < ~/.secrets/openai-key
carina auth list
carina providers list --refresh
```

Pick a runtime model explicitly when needed:

```bash
CARINA_REASONER_MODEL=openai/gpt-5 carina-daemon &
carina run --model openrouter/anthropic/claude-sonnet-4-5 "inspect this migration"
```

### Agent Modes And Slash Commands

Discover reusable agents and commands at runtime:

```bash
carina agents list
carina commands list
carina run --agent plan "inspect the release risk"
carina run "/review main"
```

Built-ins include `build`, `plan`, `general`, and `explore`. User and project
overrides live under `~/.carina/agents`, `<repo>/.carina/agents`,
`~/.carina/commands`, and `<repo>/.carina/commands`.

Prompt skills use progressive disclosure. Put a skill in
`~/.carina/skills/<name>/SKILL.md` or
`<repo>/.carina/skills/<name>/SKILL.md`, then invoke it explicitly as `$name`
inside a task or as `/name` when no existing slash command has that name.
Existing commands always win collisions. Example:

```markdown
---
name: security-review
description: Review a change for concrete security risks.
when-to-use: Authentication, authorization, secrets, or untrusted input.
user-invocable: true
implicit-invocation: true
triggers: [security audit, threat model]
allowed-tools: [read, search]
---
Inspect the requested change. Trace each finding from source to sink and cite
the affected files.
```

Only bounded metadata is always present in the model prompt; the full body is
loaded for an explicit mention. Implicit matching is off by default and uses
only exact declared triggers when enabled with
`CARINA_IMPLICIT_SKILL_PROMPTS=true`. Disable skills fail-closed with
`CARINA_DISABLED_SKILLS=name-a,name-b`. `allowed-tools` is non-granting
guidance: the selected agent profile and capability kernel remain authoritative.

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
| Cloud-hosted agent tasks | OpenAI Codex cloud tasks and managed agent services | Carina is local-first. Cloud identity and multi-endpoint sync live behind Nebutra Cloud boundaries, not inside the local runtime. |
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
8. Persistent memory writes are capability-gated, scoped, bounded, and audited.

Alpha limitations:

- Carina is not a VM or complete container isolation system by itself.
- OS sandbox backends exist, but production profiles need deployment review.
- Policy correctness depends on routing commands through the Carina daemon and
  toolchain.
- Tag-release automation fails closed on Developer ID signing and Apple
  notarization. A release is only considered notarized when its release page
  contains Apple-accepted notary JSON and the generated signing report.

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

Build a local release candidate archive:

```bash
make release-package
```

More documentation:

- [Product positioning](docs/product.md)
- [Brand and design system](docs/brand/README.md)
- [Nebutra Cloud boundary](docs/nebutra-cloud-boundary.md)
- [Roadmap](docs/roadmap.md)
- [Release process](docs/release.md)
- [Architecture](docs/architecture.md)
- [RPC API](docs/rpc-api.md)
- [Plugin model](docs/plugin-model.md)
- [Research status](docs/research/absorption-status.md)

## License

MIT License. See [LICENSE](LICENSE).
