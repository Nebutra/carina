# Carina Agent CLI/TUI Productization Plan

Status: **accepted plan** — research synthesis, no code changes yet. This is
the definitive plan for turning Carina's thin client surfaces
(`apps/carina-cli`, `apps/carina-tui`) into a product, derived from a full
reverse-engineering pass over Claude Code's internals
(`~/workspace/assets/references/claude-code-notes`, 9 chapters) cross-checked
against a line-level audit of Carina's current CLI/TUI/daemon surfaces.

Method: 取其精华，去其糟粕 — absorb the essence, discard the dross. Every
absorbed pattern below is *adapted to Carina's identity*, not copied; every
rejected pattern is documented with the reason (§5), because what we
deliberately do not take is part of the design.

---

## 1. Identity and hero moments

Carina is a **local-first, governed runtime for coding agents**: a Go daemon
that owns sessions and tools, a Rust kernel that is the *sole* policy/audit
chokepoint (hash-chained audit, per-action `PermissionDecision` with
`decision_id`), and Zig native tools on a <100ms passthrough path. BYOK:
every user pays for their own tokens. Terminal-native: the CLI and TUI are
the product, not an afterthought to a web app.

Carina is **not a Claude Code clone**. Claude Code's permission logic lives
in the user-controlled client process; Carina's lives behind a process
boundary in the Rust kernel. That single architectural fact drives almost
every adaptation in this plan: things Claude Code must do defensively in the
client (schema `.omit()`, duplicated UI-side shell parsers, anti-debugging),
Carina gets structurally for free — and things Claude Code cannot offer
(per-subcommand audit lines, cryptographically anchored session lineage,
ground-truth context accounting), Carina can make hero moments.

### Hero moments — where the product must be visibly better

1. **The approval moment.** An agent wants to do something consequential; the
   kernel says "ask". The prompt the human sees must contain the reviewable
   artifact itself (colored diff for a patch, canonicalized command for exec,
   plan text for a mode change), the policy rule that fired, and structured
   scope options. Today this moment is a raw JSON blob a user must notice by
   eye (§2). It is the single largest gap.
2. **The audit narrative.** "Why was this allowed?" / "What did my Ctrl-C
   kill?" / "Whose key ran this?" must always be answerable from the
   hash-chained audit — and the CLI/TUI must make asking those questions
   one command away.
3. **Patch review.** Propose → review (real diff viewer) → approve → apply →
   rollback, as one guided flow over the already-shipped transactional
   `workspace.patch.*` lifecycle.
4. **Multi-agent sessions under one governance roof.** Session tree, worker
   fleet, per-agent badges on every event and approval — daemon-mediated,
   audit-logged coordination (not filesystem mailboxes).
5. **BYOK honesty.** First-class cost surface, deterministic credential
   precedence, degrade states that name the failing provider and the fix.

Design stance: **governance-visible UX is the differentiator.** Claude Code
hides its machinery to feel magical; Carina *shows* its machinery to feel
trustworthy — and spends its personality budget (microcopy engine, §4) making
that machinery pleasant to read.

---

## 2. Current-state gap map

Condensed from the surface audit. Full detail lives in the audit record; this
table is the working map.

### 2.1 Exists (foundations to build on, not rebuild)

| Area | State |
|---|---|
| CLI surface | ~40 subcommands in `apps/carina-cli/main.go` (~1250 lines): run/ask, sessions, resume, watch, audit verify/replay/last, patch lifecycle, approve/deny, memory, context, auth BYOK, gateway, exec |
| Resume UX | The one designed experience: `cmdResume`/`printResumeSummary`, `--watch/--json/--no-input`, last-5 summary, "continue:" hints |
| Event transport | `session.events.stream` JSON-RPC notifications (`go/daemon/bus.go`, `go/rpc/server.go`) consumed by `carina watch` |
| Governance plumbing | Daemon-side complete: `requires_approval` pause, `decision_id`, `pendingCmds`, 5m timeout, `task.action.approve/deny` with role + dynamic scope (`go/daemon/daemon.go`, `go/daemon/approval.go`) |
| Audit CLI | `audit verify`, `audit last`, report, export |
| Patch lifecycle | propose/show/apply/rollback wired to `workspace.patch.*` |
| Native fast path | scan/grep/diff/pty passthrough to Zig binaries, no daemon dial |
| Item stream | `session.items` normalized thread/turn/item events — the foundation for a TUI transcript |
| Daemon ops | Layered config cascade, SIGHUP + file-watch hot reload, unix socket + optional TCP/HTTP/WS gateway |

### 2.2 Stubbed (exists in name, not in experience)

- `carina-tui` is a 125-line one-shot printer: no event loop, no input, no
  live refresh.
- `carina run` submits and exits — never shows the answer; `--background`
  silently dropped.
- `carina watch` is a debug tail of raw JSON blobs.
- **Approvals are one-directional**: daemon pauses and publishes; no client
  renders a prompt; operator must extract `decision_id` by eye and approve in
  another terminal.
- `patch show` dumps JSON — the patch-review hero moment has no viewer.
- Degrade reasons computed daemon-side (`go/daemon/agent.go` `d.degrade`)
  arrive as undifferentiated JSON.
- No streaming anywhere (`go/model-router/router.go` defers it); tool events
  bluntly truncated at 400 chars.
- Help is one static 80-line string; PRD §11 commands absent (`carina edit`,
  `carina plan`, `carina daemon start|stop|status|logs`, `carina worker …`).

### 2.3 Missing (whole layers)

Interactive TUI/REPL; streaming render pipeline; approval prompt UI and
`carina approvals`; diff review interface; session picker (resume requires a
memorized id); statusline/chrome; microcopy/error style; theming and
NO_COLOR; i18n (all strings English while product docs are Chinese);
notifications (bell/OSC 9); governance-distinct exit codes; explicit
`--json/--plain` contract; TTY-awareness; first-run onboarding/doctor;
multi-agent dashboard; shell completions; `carina config get/set`;
Ctrl-C → `task.cancel` mapping (RPC exists, `docs/rpc-api.md` line 183; no
CLI mapping).

---

## 3. Absorb/adapt roadmap — three phases

Each item names its **source pattern(s)** from the Claude Code analysis and
its **Carina fit** (why/how it lands differently here). Ordering within a
phase is dependency order. Every item is scoped to be cuttable into an
implementation workflow as-is.

### Phase 1 — See, Decide, Trust (must-have terminal UX)

The theme: a user in a terminal can *see* what agents are doing, *decide* at
the approval moment, and *trust* that interrupts, failures, and outputs are
truthfully represented. Everything here closes a "stubbed" or "missing" row
in §2 using plumbing that already exists.

#### P1.1 Kernel-backed approval surface (the flagship) — **Shipped**

*As-built: kernel `patchGates` + `go/daemon/approval.go`'s*
*`awaitInteractiveApproval`/`signalPendingApproval` single choke point, the*
*`go/tui` approval overlay rendering the real reviewable artifact (colored*
*unified diff for `PatchApply` via `ev["diff"]`), and a real*
*`task.action.approve`/`task.action.deny` round trip that resolves the same*
*wait whether it arrives via `task.approval.resolve` or the general RPC*
*surface. Landed in f1ba5cf.*

*Source: Leader Permission Bridge + shouldDefer reviewable payloads +
AskUserQuestion structured options + grant scoping + hidden approval-field
invariant — merged.*

Closes the largest gap: the daemon already pauses on `requires_approval` and
publishes `decision_id` events, but no client renders a prompt. Build:

1. **`carina approvals`** — RPC listing pending decisions with agent badge,
   action, and the policy rule that fired.
2. **Inline prompt** in foreground runs and (later) the TUI: a structured
   2–4 option question — approve once / session / project / deny-with-reason
   (auto free-text escape) — whose *body is the reviewable artifact*:
   colored unified diff for patch decisions (this doubles as the missing
   patch-review viewer), canonicalized command for exec, plan text for mode
   changes.
3. **Scoped grants are audited policy mutations**: the scope choice persists
   as an audit-chain event, so "why was this allowed without asking" is
   always answerable — a gap Claude Code itself has.
4. **Rust-kernel invariant**: approval-bound payload fields are settable only
   by the daemon's approval resolver; agent-originated requests carrying them
   are rejected. Stronger than Claude Code's client-side schema `.omit()`.

Carina fit: the Leader Bridge collapses to nothing extra — the kernel already
*is* the bridge. Skip Claude Code's classifier tier (make it an explicit
policy rule) and its mailbox fallback (the daemon is always the mediator).

#### P1.2 Canonicalize → validate → decide tool pipeline — **Shipped**

*As-built: `go/toolnorm` (`Canonicalize`/`Validate`) is the shared,*
*side-effect-free normalizer — path expansion, fixed-point stripping of*
*env-prefixes and no-op-for-policy wrapper commands (`timeout`, `nice`,*
*`env`) — wired into `go/daemon/agent.go`'s `agentRun` ahead of the kernel*
*decision: `Validate()` rejects empty/unresolvable/workspace-escaping*
*commands with a teachable `{code, message}` and never dials the kernel or*
*touches the audit chain for that rejection; a validated command's*
*`WrapperStripped` form is what `crates/carina-policy` classifies and*
*`canon.Command`/`canon.Argv` are what the audit chain and the actually*
*executed process both use, so audit and execution can never diverge on*
*phrasing. Scope reduction vs. the original text: the explicit*
*`sandboxed: yes/no` result-envelope field is not yet threaded through (no OS*
*sandbox exists to report on yet); tracked as a Phase 2/3 follow-up when an*
*OS-level sandbox lands, not silently dropped — the precedence rule itself*
*(policy verdicts outrank any future sandbox) is already the only order*
*`agentRun` implements.*

*Source: input normalization + two-phase validateInput/checkPermissions +
sandbox-precedence principle — merged.*

Kernel-integrity **prerequisite** for P1.1 and P2.1:

- One shared normalizer in the Go daemon tool layer: path expansion
  (`~`/relative → absolute), fixed-point stripping of env-prefixes and
  wrapper commands (`timeout`, `nice`), so `crates/carina-policy` matches
  canonical forms only and the audit chain records the canonical form
  actually authorized.
- A side-effect-free `validateInput` stage *ahead of* the kernel decision
  returns teachable `{errorCode, message}` tool_results so the model
  self-corrects without burning a human approval. Users are never asked to
  approve garbage; the audit records decisions, not typos.
- Document and encode precedence: **policy verdicts outrank any future OS
  sandbox**; `sandboxed: yes/no` joins degrade-status in the result envelope
  (deferred — see as-built note above).

#### P1.3 Failure-state and degrade taxonomy, surfaced end-to-end — **Shipped**

*As-built: the degrade-status enum and initiator field are computed*
*daemon-side and rendered through `go/microcopy`'s Degrade register (the*
*shared engine P1.7 ships); `go/tui` and the CLI's `classifyExitCode`*
*(`apps/carina-cli/exitcode.go`) consume the same `tui.Outcome` enum so a*
*degrade never silently collapses to a generic failure. Landed in f1ba5cf.*

*Source: failure microcopy taxonomy + expected-kill suppression + glanceable
tri-state vocabulary — merged.*

Cheapest high-visibility win. Extend the degrade-status enum with
`interrupted | timed_out | backgrounded | conflict | done` plus an
`initiator = user | policy | agent` field. Clients render distinct labels via
the microcopy engine's Degrade register: "stopped by you" (quiet), "stopped
by policy `<rule>`" (loud), "Done" for no-stdout commands instead of
"(No output)". Suppress expected-kill noise in model context and TUI but
**never in the audit chain** — the kill, its initiator, and partial results
are recorded unconditionally. Adopt the four-glyph status vocabulary —
`✓ ok / ⚿ needs-auth / ✗ failed / ~ degraded` — across all status surfaces
(exact glyphs are a brand question, §6).

#### P1.4 Cascading interrupt as an audited governance event — **Shipped**

*As-built: the Ctrl-C cascade (two presses inside a bounded window exit the*
*TUI; `task.cancel` mapping) is wired end-to-end and verified under a real*
*PTY harness (`go/tui/pty_integration_test.go`, tmux-backed) — the terminal*
*is confirmed never left in raw mode and the governance exit code is*
*asserted on screen. Landed in f1ba5cf.*

*Source: interrupt/abort patterns (both) — merged.*

Daemon owns a `context.Context` cancellation tree (session → agents → tool
processes) so one cancel reliably reaps the whole agent tree. Map Ctrl-C
during foreground `carina run`/`watch` to the `task.cancel` RPC that already
exists but has no CLI mapping. Write one audit event per killed node plus
synthetic `tool_use_interrupted` transcript records so both the model and the
auditor see exactly what an interrupt stopped. Hero framing Claude Code
cannot match: *the audit trail shows what your Ctrl-C killed.*

#### P1.5 One engine, two renderers + pipe-mode approval frames + exit codes — **Shipped (with documented v1 pipe-mode scope reduction)**

*As-built:*

- *(a) One engine, two renderers, bare-invocation TTY switch: bare `carina`*
  *(no subcommand) now decides via `decideBareInvocation` — real TTY on both*
  *stdin and stdout launches the interactive Bubble Tea TUI in-process*
  *(`runBareTUI` in `apps/carina-cli/bare_launch.go`, importing `go/tui`*
  *directly exactly as `apps/carina-tui` does, no exec/fork); anything piped*
  *or redirected preserves the original usage+exit-2 behavior. `runBareTUI`*
  *auto-starts `carina-daemon` on an unreachable socket*
  *(`ensureDaemonReachable`, bounded retry/backoff) and resumes the most*
  *recent session for `cwd` via `resumeMostRecentOrFresh`, falling through to*
  *a fresh session exactly as `carina run` already did. Verified end-to-end*
  *under a real tmux PTY harness against a live daemon+kernel+Zig-tools*
  *stack for this closure workflow: bare `carina` ran the first-launch*
  *`carina doctor` auto-check once, attached to a fresh session, rendered the*
  *transcript/prompt chrome, and a Ctrl-C Ctrl-C cascade exited 0 leaving the*
  *pty in canonical (non-raw) mode.*
- *(b) Governance-distinct exit codes: `classifyExitCode`*
  *(`apps/carina-cli/exitcode.go`) maps every one-shot command's terminal*
  *error onto the SAME `tui.Outcome` enum `go/tui/model.go` already defines*
  *and `apps/carina-tui` already reuses — not a second enum. `carina run`/`ask`*
  *now block in the foreground by default until the task reaches a terminal*
  *state and exit with that state's governance code; `--background` opts out.*
  *Frozen enum: OK / usage / runtime error / policy-denied / user-denied /*
  *daemon-unreachable / degraded-partial (doctor's WARN/FAIL folds into*
  *degraded-partial via `doctorOutcomeError`).*
- *(c) Pipe-mode frames — v1 scope reduction, tracked as a follow-up rather*
  *than silently dropped: `carina watch --json` emits typed, one-directional*
  *`{"frame":"control_request", decision_id, capability, resource, reason,*
  *label, diff, session_id, task_id}` NDJSON frames*
  *(`controlFrameForEvent` in `apps/carina-cli/watch_json.go`) whenever a*
  *`permission.request` event streams past, so a CI bot/wrapper script can*
  *grep stdout for `frame=control_request` without a TTY. The plan's*
  *originally-scoped bidirectional half — accepting a typed*
  *`control_response` back on the same process's stdin — was deliberately*
  *NOT implemented in v1: the already-shipped, already-tested*
  *`carina approve`/`carina deny <session_id> <decision_id>` commands are the*
  *v1 resolution path (a second concurrent process call), which is*
  *sufficient for the CI-bot/wrapper-script use case P1.5(c) exists for and*
  *reuses proven code instead of adding a new stdin-framing protocol surface*
  *this workflow's scope did not require. Follow-up: fold*
  *`control_response` stdin framing into `carina watch --json` directly if a*
  *future single-process pipe-mode caller needs it.*

*Source: headless/interactive thin-skins + dual-mode commands + NDJSON
control protocol — merged and reframed.*

Formalize what is currently accidental:

- Every command = one daemon RPC data layer + two renderers: TTY human view
  and `--json` with a schema version. Default chosen by `isatty`. Identical
  permission-decision and degrade semantics in both — scripts see exactly
  what humans see.
- The NDJSON pattern is *reframed*, not copied: Carina's JSON-RPC socket
  already gives bidirectional correlation. The absorb is: in pipe/print
  mode, emit typed frames including `control_request{decision_id}` and accept
  `control_response` on stdin — **governance scriptable for CI bots and
  wrapper UIs without a TTY**. Arguably more central to Carina's identity
  than to Claude Code's.
- **Governance-distinct exit codes** (audit-flagged as missing): e.g.
  `0` ok, `1` runtime error, `2` usage, `3` policy-denied, `4`
  approval-timeout, `5` daemon-unreachable, `6` degraded-partial. Frozen in
  docs as a compatibility contract.

#### P1.6 `carina doctor` — three-state diagnostics with copy-paste fixes — **Shipped**

*As-built: `carina doctor [--json]` (`apps/carina-cli/doctor.go`) renders*
*PASS/WARN/FAIL for kernel reachability, state-dir writability, Zig*
*native-tool presence, the reasoner, the context engine, per-language LSP*
*server presence (`go/daemon/doctor_probes.go`'s `lspProbe`, with a*
*copy-paste install command per missing server), per-provider BYOK key*
*resolution (`byokProbe`, store-then-env precedence), and enterprise*
*policy-bundle freshness (`policyBundleStale` — catches the case where*
*`bundle.toml`/`trusted-keys`/`approval.json` changed on disk since the*
*daemon's last restart, since `ApplyConfig`'s SIGHUP/config reload*
*deliberately never re-inits kernel/policy wiring). Honors the*
*`CARINA_DOCTOR_DISABLE` kill-switch for locked-down deployments (returns a*
*minimal disabled report without touching the kernel or any credential).*
*Runs automatically, once, on the first bare `carina` launch on a machine*
*(`maybeAutoRunDoctor`, exercised in this workflow's live PTY smoke test) to*
*double as onboarding — never repeats after that first run.*

*Source: doctor/diagnostics pattern.*

Mostly wiring of probes that already exist: daemon socket dial, Rust kernel
version handshake, Zig native-tool presence, LSP probes
(`go/daemon/lsp_probe.go`), index freshness (`crates/carina-index`),
audit-chain head verification (`audit verify` already shipped), provider key
resolution per BYOK backend. Render pass/warn/fail with a concrete
remediation command per failure ("Run: `carina-daemon &`"). Honor a
kill-switch env for locked-down deployments. **Run automatically on first
launch** to double as onboarding (audit flags onboarding as absent).

#### P1.7 Microcopy engine v1 — Governed + Degrade registers, en + zh — **Shipped**

*As-built: `go/microcopy` ships the deterministic Governed and Degrade*
*registers consumed by P1.1's approval overlay and P1.3's degrade-status*
*rendering, `microcopy.DetectLocale()` locale detection (wired into*
*`runBareTUI`), and suppression for non-interactive/`--json`/`--plain`*
*output paths per `docs/brand/brand-brief.md` §4 — governance moments always*
*use the Governed register, never Ambient. Ambient register and LLM*
*widening remain P3, as originally scoped.*

Full spec in §4. Phase 1 ships the deterministic core: `go/microcopy`
package, Governed and Degrade registers (the ones P1.1 and P1.3 consume),
locale detection, suppression rules for `--json/--plain/!isatty`. Ambient
register and LLM widening are P3.

#### P1.8 Integrity and ordering hardeners (small, near-zero cost, do first) — **Shipped**

*As-built (all three land and are pinned by dedicated tests):*

- *Write-ahead: `handleTaskSubmit`'s synchronous sequence — kernel*
  *audit-chain append of `TaskCreated` carrying the submitted prompt, then*
  *durable `persistRun` — completes strictly before the reasoner goroutine's*
  *first dispatch can run, pinned by*
  *`go/daemon/writeahead_order_test.go`'s `TestWriteAheadTaskCreatedPrecedesReasonerDispatch`*
  *(a synchronization gate via a blocking reasoner double, not a timing*
  *assumption).*
- *Single-writer drain: `go/rpc/server.go`'s `connWriter` is the one writer*
  *goroutine per connection — every frame (request responses from*
  *`serveWithScopes` and notifications from any `Subscription.Notify`) funnels*
  *through one buffered channel and is encoded strictly in enqueue order,*
  *closing the exact race the plan called out (an approval event overtaking*
  *the tool-call event it governs). Pinned by*
  *`go/rpc/single_writer_drain_test.go`. Shutdown ordering is also*
  *hardened: `serveWithScopes` blocks on the writer's `stopped` channel*
  *before closing the connection, so an in-flight `Encode` can never be*
  *silently dropped by a racing `conn.Close()`.*
- *Startup discipline: `apps/carina-cli/main.go`'s dispatcher gates*
  *help/version/completion and the native Zig passthrough*
  *(`scan`/`grep`/`diff`/`pty`/`run-native`/`patch-native`) before the shared*
  *`initGate` call — those paths never touch socket, config, or kernel.*
  *Every governed subcommand shares one `initGate`. A companion hardener not*
  *in the original text but required to make auto-start*
  *(P1.5(a)) safe: `go/rpc/server.go`'s `ListenUnix` now acquires a*
  *cross-process advisory `flock` (`ErrSocketInUse`) before binding, so two*
  *racing `carina-daemon` auto-starts on a fresh machine fail closed instead*
  *of one silently stealing the other's live socket.*

**Phase 1 exit criteria — confirmed holding, all four clauses:** a foreground
`carina run` streams events with rendered microcopy (P1.3/P1.7), pauses
inline on an approval with a visible diff (P1.1, real unified diff for
`PatchApply` decisions), honors Ctrl-C as an audited cancel (P1.4, PTY-verified
cascade with a clean non-raw-mode exit), exits with a governance-distinct
code (P1.5(b), `classifyExitCode` → shared `tui.Outcome`), and `carina doctor`
gets a new machine from zero to green (P1.6, auto-runs once on first bare
`carina` launch; live-verified in this workflow's PTY smoke test — a fresh
state dir produced an all-PASS-or-expected-WARN report with copy-paste
remediation for each WARN). **Phase 1 is fully complete**: P1.1–P1.8 are all
shipped, `cargo test --workspace` and `go test ./...` are green, and the bare
`carina` invocation is confirmed under a live tmux PTY harness against a real
daemon + Rust kernel + Zig tools to enter the interactive TUI rather than
print usage.

### Phase 2 — Governance differentiation (things Claude Code structurally cannot offer)

#### P2.1 Per-subcommand permission decisions via shell AST, cap-to-ask

*Source: shell-parser permission splitting + escalation caps.*

The deepest extension of the hero moment. Parse compound Bash into
simple-command nodes (tree-sitter-bash or `mvdan/sh` in `go/toolchain`);
obtain an independent kernel verdict per node — `cd /x && python3 evil.py`
cannot ride a `cd:*` rule. Any deny fails the whole command; >50 nodes
escalates to the existing `decision_id` ask flow instead of hard-reject.
**Each subcommand verdict becomes its own line in the hash-chained audit** —
per-subcommand audit granularity is structurally impossible for Claude Code.
Later layer: flag-granular typed allowlists (`xargs -I` ok, `-i` deny) in the
`protocol/capabilities` rule schema, with the denying flag and reason named
in the prompt copy.

#### P2.2 Session forking with audit-anchored lineage; rewind as append

*Source: /branch fork + rewind semantics.*

Natural successor to shipped resume. Fork copies the transcript to a new
`sessionId` with `forkedFrom{sessionId, chainPosition}` recorded in the audit
chain — **cryptographically anchored session ancestry**, which Claude Code
lacks. Rewind truncates working history but the chain records the rewind
event rather than rewriting. Sanitize derived titles (collapse newlines,
100-char cap). Also fixes the audit-flagged gap that resume requires a
memorized id: fork/resume pickers get human-readable titles.

#### P2.3 BYOK cost and token accounting surface

*Source: /cost display inverted + ProgressTracker asymmetry + sticky
cache-latch principle.*

Invert Claude Code's dollar-hiding: every Carina user pays per token, so
`carina cost` is first-class, fed by `go/model-router` per-provider
accounting split across main model / embeddings / rerank (Carina now runs
all three). Apply the correctness rule naive implementations miss:
**input_tokens is cumulative per API round (keep latest); output is per-round
(sum)** — inflated cost displays destroy trust in a BYOK product.
Per-session reset plus lifetime local history (local-first lets Carina offer
what Claude Code punts to a billing portal). Adopt sticky cache-shape
latching (stable headers/tool-schema ordering per session per provider) as a
router principle, with a cost-report note when a latch engages — the savings
accrue to the user's own bill.

#### P2.4 BYOK credential precedence chain + `carina auth status`

*Source: credential resolution chain, pruned.*

One documented deterministic order per provider in `go/model-router` and
`go/auth`: flag > env > keychain/helper > config. `carina auth status` names
the winning source per provider; the resolved source is recorded per-session
in the audit trail so **"whose key ran this action" is always answerable**.
Drop the OAuth rungs and managed-context gates until a managed surface
exists.

#### P2.5 Compaction as visible, audited events + `carina context` ground truth

*Source: graduated-compaction UX contract + /context accuracy — merged.*

Carina has Headroom natively, so skip Claude Code's four-level pipeline and
absorb the *contract*: every compaction emits a visible, audit-logged event
("folded 14 tool results, ~38K tokens reclaimed") with the pre-compaction
transcript retained on disk. In a governed runtime, context edits are state
changes users can inspect, not housekeeping. `carina context` reports the
daemon's *actually-assembled* prompt breakdown — because Headroom assembles
prompts daemon-side, Carina reports ground truth where Claude Code must
simulate.

#### P2.6 Command metadata registry: trichotomy + two-axis filtering

*Source: command trichotomy + capability/state gating + immediate bit.*

Re-found carina-cli's ~40 subcommands (currently one static usage string) on
a metadata registry:

- **local** commands = daemon RPCs;
- **prompt** commands = governed session launches whose tool allowlists
  register as kernel-side session-scoped policy overlays
  (activation/deactivation audited — hardening Claude Code's client-side
  convention);
- **interactive** commands = TUI views.

Filter visibility on two axes: capability from `protocol/capabilities.json`
and live daemon state — a degraded daemon *visibly narrows* the surface
instead of failing at call time. Tag read-only RPCs `immediate`;
session-mutating ones serialize against the session's turn loop (attribute in
`go/rpc`). Yields per-command help and a future TUI palette from one source.

#### P2.7 Daemon-mediated typed agent coordination

*Source: mailbox messaging + turn-boundary queueing (×2) + structured task
notification + idempotent notified flag — merged.*

Route inter-agent messages through the daemon (it owns the socket and session
registry), **not filesystem mailboxes**: same typed-union message design and
`request_id` idempotency, but delivery becomes observable and audit-loggable.
Queued steering messages drain at turn boundaries (deterministic injection
points; each arrival/consumption audit-logged; transcript stays replayable).
Worker completion is a typed protocol event — not XML-in-chat — whose
envelope carries audit fields: final chain hash, permission-decision count,
approved/denied `decision_id`s. Terminal-state transition is a single CAS in
daemon state so exactly one "session terminated" record exists. TUI renders
worker reports in bordered blocks with agent badges.

#### P2.8 Dirty-write protection: read-credential + atomic freshness check

*Source: file-freshness / forced-read invariants.*

Enforce daemon-side and in the Zig patch tools: edits require a prior full
read (partial views insufficient); mtime/content checked twice — friendly
early error in validation, then synchronously inside the atomic
read-compare-write section. Conflicts surface as a distinct degrade status
("file changed externally — re-read required") with a prescriptive recovery
message. In multi-agent sessions this invariant is what prevents cross-agent
clobbering — something Carina *uniquely* needs.

#### P2.9 Tail-preserving truncation + persist-and-reference

*Source: output truncation strategy + oversized-result persistence.*

Replace the blunt 400-char event truncation (`go/daemon/agent.go` ~line 752)
with head-discarding, tail-keeping accumulation (errors live at the end).
Persist oversized results under the session directory with a reference in
model context. Set `appliedLimit` only when truncation actually occurred, so
agents paginate `code.search`/`code.refs` instead of re-running — and never
believe a truncated search was exhaustive. Local-first storage makes
persist-and-reference nearly free.

#### P2.10 Config-cascade conflict warnings + versioned audited migrations

*Source: settings-conflict warnings + migration versioning — merged.*

Carina's cascade is deeper than Claude Code's (org policy > env > project >
user > session) because it is governance-first: every write through config
paths reports which layer won; warnings fire only on actual conflict;
policy-layer overrides name their policy source — a UX courtesy upgraded into
**governance legibility**. Add version-gated idempotent migrations for
config/session-store/capabilities.json, with the twist that migrations
touching permission policy are themselves audit-chain events, since they
change governance semantics.

#### P2.11 Fail-open metrics vs fail-closed audit — stated explicitly

*Source: nil-safe telemetry writers.*

Adopt nil-safe no-op-on-failure semantics for optional telemetry/counters
only. The Rust kernel's hash-chained audit is the opposite contract: **if an
audit append fails, the governed action does not proceed.** Encode the
asymmetry as an explicit architectural rule in docs and code structure
(separate writer interfaces) so the fail-open posture can never leak into the
audit path.

**Phase 2 exit criteria:** a compound shell command produces N audit lines
with independent verdicts; `carina cost` and `carina auth status` answer the
two BYOK trust questions; forked sessions carry verifiable ancestry;
`carina context` matches the prompt the daemon actually sent.

### Phase 3 — Delight (the TUI, ambient personality, resilience polish)

#### P3.1 TUI foundation cluster — architecture locked before line 126

*Source: command-as-thin-shell components + deferred prefetch after first
paint + early input capture + kernel-verdict collapsed rendering — merged.*

Decisions to lock in **before** `apps/carina-tui` grows past its current 125
lines (the contract is declared in P1 docs; the build lands here):

- Every view (approval queue, audit browser, patch review, session graph) is
  a component over a daemon RPC *shared with the CLI renderer* — one data
  layer, two skins (extends P1.5).
- Paint the input frame first; background-load index warmup, git status,
  audit-head verification, and session list afterward. Headless skips all of
  it.
- Buffer stdin from process start and replay into the input model on first
  frame — and **never arm capture on paths without an input box** (the
  negative rule from the rejected standalone item, §5.8).
- Collapse transcript entries whose kernel policy verdict was
  read-only-allow: one authoritative classifier (the kernel) instead of
  Claude Code's duplicated UI-side shell parser — and the visual hierarchy
  doubles as an at-a-glance governance display.

First four views, in order: approval queue (P1.1 data), patch review diff,
session picker/graph (P2.2 data), audit browser.

#### P3.2 Background task pill + two-axis task state model

*Source: task pill + status/isBackgrounded decoupling + completed-task grace
window — merged.*

The TUI's first real widget, and the CLI statusline's data source: a daemon
`/status` RPC returns structured pill segments **computed server-side** so
both clients render the same truth. Governance states lead the vocabulary:
"awaiting approval (`decision_id`)" and "~ degraded: index stale" outrank
"2 agents running". Model tasks with orthogonal `status` (lifecycle) and
`ui_placement` fields so backgrounding never touches execution and resume
restores both; `awaiting_approval` escalates into the pill regardless of
placement. Grace-window eviction with microcopy leaning on the audit asset:
"removed from view — full record in audit `<id>`".

#### P3.3 Client-daemon reconnect state machine

*Source: reconnect/backoff machinery, pruned.*

For carina-cli/tui ↔ daemon unix-socket sessions: exponential backoff on
daemon restart; policy-denied/authz failures from the kernel classified
**permanent** (never retried); a >60s inter-attempt gap resets the retry
budget so laptop wake doesn't inherit an exhausted budget — Carina daemons
live on laptops. Skip the WebSocket/SSE machinery until the gateway becomes a
real remote surface.

#### P3.4 Ambient microcopy + LLM pool widening + zh polish

The Ambient register (§4) ships here: seeded deterministic spinners for
`carina run` streaming, index warmup, session load; `carina microcopy
refresh` as a governed, audited, BYOK-spending action; per-session themed
pools (opt-in). This is the rescued kernel of the rejected buddy pattern
(§5.2): personality as deterministic, auditable voice.

#### P3.5 Terminal-citizen extras

Closing the remaining audit "missing" rows, all small once P1/P2 exist:
terminal bell + OSC 9/777 notifications when a background session blocks on
approval or completes (critical for the "tasks survive CLI exit" model);
NO_COLOR/light/dark themes over one palette table; shell completions and man
pages generated from the P2.6 registry; `carina config get/set/list` over the
existing cascade with P2.10 layer-attribution; pager for long audit replays.

---

## 4. Microcopy engine — design spec

Reference analyzed: Claude Code's loading-microcopy module (context pools ×
locale, regex seed-overrides, FNV-1a seeded deterministic pick,
`isLoadingMicrocopy` membership test).

### 4.1 Location and consumers

Go package **`go/microcopy`** (peer of `go/rpc`, `go/config`), imported by
`apps/carina-cli`, `apps/carina-tui`, and `go/daemon`. Division of labor:
**the daemon emits stable machine codes only** (degrade-status enums, event
types, decision metadata) — it never emits prose; clients map code → copy via
this package. This keeps the wire protocol and audit chain language-neutral
and lets `--json` output stay copy-free. Embedded default pools live in
`go/microcopy/pools/*.json` compiled in via `go:embed`; an optional user
overlay lives at `~/.carina/microcopy/pools.v{N}.json`.

### 4.2 Three registers, type-segregated (the governance tone hook)

The single most important divergence from the reference: not all strings are
equal in a governed runtime. Three registers as **distinct Go types**, so a
playful line can never leak into a decision prompt — enforced by the
compiler, not convention:

- **Ambient** (playful, reference-style pools): spinners, prefetch, index
  warmup, session load.
  `func Loading(seed string, opts ...Option) string`
- **Governed** (sober, template-based, zero whimsy): permission prompts,
  approval queue entries, policy denials, audit event summaries.
  `func Governed(code Code, args Args) string` — slot-filled constant
  templates, e.g.
  `"Approval required: {action} — rule {rule_id} matched 'ask'. [a]pprove once  [s]ession  [p]roject  [d]eny (reason)"`.
  Anything that asks for a decision or lands in the audit narrative uses this
  register.
- **Degrade** (calm-factual + remedy):
  `func Degrade(status DegradeStatus, args Args) string`, e.g.
  `"~ Degraded: reranker offline — results unranked. Fix: carina doctor"`.
  Maps 1:1 onto the daemon's existing degrade-status header enum
  (`go/daemon/agent.go`), extended per P1.3 (`interrupted / timed_out /
  backgrounded / conflict / done`, plus `initiator=user|policy|agent`).

Membership test `func Is(value string) bool` spans all registers
(replace-placeholder-vs-real-content decisions in the TUI, mirroring
`isLoadingMicrocopy`).

### 4.3 Contexts and seeds

Contexts are Carina-native: `agent, policy, approval, audit, patch,
codeintel, session, provider, file, git, terminal, kernel, generic`.
`CONTEXT_PATTERNS` regexes match Carina's actual namespaces, e.g.:

- approval: `/approve|deny|decision|permission/i`
- patch: `/patch|diff|rollback|apply/i`
- codeintel: `/code\.(search|symbols|map|def|refs|impact)|index/i`
- audit: `/audit|chain|verify|replay|export/i`

Seed derivation is mechanical and documented: **seed = the RPC method or tool
name as it appears on the wire** — `"code.search"`,
`"workspace.patch.apply"`, `"task.action.approve"`, `"session.resume"` —
optionally salted with session id (`seed = method + "|" + sessionID`) when
per-session variety is wanted while keeping within-session stability. Degrade
seeds are the degrade reason code itself.

**Determinism contract:** same seed + locale + pool-version always yields the
same line — spinner text is snapshot-testable, and a user re-running the same
command sees the same personality, which reads as intentional rather than
random.

Hash: FNV-1a 32-bit via stdlib `hash/fnv` (`fnv.New32a`), byte-for-byte
compatible with the reference's `Math.imul` implementation for ASCII seeds;
`pick = hash % len(pool)`. Override chain identical to the reference: locale
regex overrides first, then context pool, then generic.

### 4.4 Locale detection

Resolution order (resolved once at client init, cached): `--locale` flag >
`CARINA_LOCALE` env > config-cascade value (rides the existing layered config
in `apps/carina-daemon/main.go`) > `LC_ALL`/`LC_MESSAGES`/`LANG` prefix parse
> `en`. Supported initially: **en, zh** (the PRD/product docs are Chinese —
zh pools are first-class, not an afterthought), with es/ja/ko slots falling
back to en.

Suppression rules: when `!isatty(stdout)`, `--json`, or `--plain`, the
Ambient register returns bare verbs ("Loading...") or empty; Governed and
Degrade registers still render (a pipe consumer deserves precise
decision/degrade text) but with glyphs stripped under NO_COLOR/plain.

### 4.5 LLM-driven extension — rules-first, never on the hot path

The deterministic embedded pools are always sufficient; the LLM layer only
*widens* them, offline:

- `carina microcopy refresh [--context X] [--locale L]` (optionally a
  `go/scheduler` job) calls `go/model-router` with a style-guide prompt +
  existing pool as few-shot, generating candidate lines. Candidates pass a
  validator: max display width ≤42 cols, no misleading action verbs (a
  spinner must not claim completion), banned-term list, locale sanity,
  uniqueness. Survivors are appended to
  `~/.carina/microcopy/pools.v{N}.json` with provenance metadata (model id,
  prompt hash, timestamp).
- **Governance framing:** the refresh is itself a governed action — it spends
  the user's BYOK tokens and mutates a product surface, so it routes through
  the kernel policy check and gets an audit record like any other model call.
  This is the Carina-shaped inversion of Claude Code's invisible
  buddy-personality generation.
- The render path never blocks on the LLM: pools load at process start
  (embedded first, overlay appended in sorted order so the merged pool is
  deterministic for a given overlay version); corrupt/missing overlay →
  silent fallback to embedded. Pool version is included in Options so tests
  pin it.
- Per-session variety (opt-in config flag): session start may request a small
  themed pool as part of the first turn through model-router; stored in
  session state, picked deterministically thereafter; absent → embedded
  fallback, no error.
- **Hard rule: the Governed register is NEVER LLM-generated at runtime.**
  Permission/approval/audit/degrade templates are code-reviewed constants;
  the refresh flow may only *propose* governed-register additions into a
  review file, never auto-activate them. A hallucinated approval prompt is a
  security bug, not a style bug.

### 4.6 API sketch

```go
package microcopy

func Loading(seed string, opts ...Option) string
func Governed(code Code, args Args) string
func Degrade(status DegradeStatus, args Args) string
func Is(value string) bool

// Option = WithLocale | WithSession | WithPlain | WithPoolVersion
```

Golden tests iterate all seeds × locales × pool versions; a fuzz test asserts
`Governed` can never return a member of an Ambient pool.

### 4.7 Where it lands first

Matching the surface audit, highest leverage: (a) `carina watch` event
rendering (event-type → Governed/Degrade lines instead of raw JSON), (b) the
approval prompt (Governed templates for `decision_id` asks, P1.1), (c)
degrade-status labels on tool results (P1.3), (d) Ambient spinners in
`carina run` once it streams (P3.4). This is the rescued kernel of the
rejected buddy pattern: Carina's personality budget spent as deterministic,
auditable voice in the places its identity lives.

---

## 5. Deliberately rejected patterns (取其糟粕 appendix)

Documented so future contributors know these were *considered and refused*,
not overlooked. Where a kernel of the idea was worth saving, the rescue is
named.

1. **Client-side anti-debugging exit.** Security-by-obstruction that only
   makes sense when permission logic lives in the user-controlled process.
   Carina's enforcement is the Rust kernel behind a process boundary —
   debugging the CLI/TUI cannot bypass policy because the client never held
   authority. Also hostile to the tinkerer audience of a local-first, open
   runtime. Carina's tamper answer is kernel enforcement plus the
   hash-chained audit.
2. **Buddy companion sprite with tamper-proof cosmetics.** Mascot cosmetics
   dilute the serious-governed-runtime positioning, and the
   canary-scanner-dodging trick is exactly the cleverness a trust-centered
   product must never ship. *Rescued kernel:* deterministic seeded
   personality survives in the microcopy engine (§4), where the personality
   budget is spent on permission prompts, audit narratives, and degrade
   notices — generated under governance instead of hidden from it.
3. **Compile-time feature-flag DCE + internal-only command stubs** (two
   first-pass entries, deduplicated). Both exist to ship one closed-source
   bundle to internal and external audiences. Carina ships one open
   local-first distribution; quietly stripping capabilities runs against its
   transparency posture and adds release-matrix complexity for zero user
   value. `protocol/capabilities.json` runtime flags plus honest
   doctor/status reporting are the right lever, and already exist.
4. **Lazy schema/command loading with memoized cwd-keyed registries.** JS
   bundle cold-start optimization; carina-cli is a compiled binary talking to
   a long-lived daemon, so registration costs nanoseconds — and the notes
   themselves document the layered-cache staleness trap this machinery
   imports. *Salvage:* cwd-scoped project discovery is better done as the
   daemon watching project config and pushing registry updates over the
   socket, keeping the CLI stateless.
5. **Audience-conditional cost hiding** (the dollar-suppression half of
   `/cost`). Rests on a flat-rate subscriber population Carina does not have.
   Every Carina user is BYOK and pays per token; hiding dollars would hide
   the user's own spend. *Inverted and absorbed* as the first-class BYOK cost
   surface (P2.3); only the hiding mechanism is rejected.
6. **`/btw` zero-trace sidecar queries.** Premature: there is no REPL or
   interactive loop to sidecar from — `carina run` submits and exits.
   Revisit when the interactive loop lands, and then as a daemon-spawned
   ephemeral sub-session that keeps zero-trace semantics *for the transcript
   only*: it still gets an audit record, because invisible compute is
   off-identity for Carina.
7. **Shell inlining in prompt templates** (`` !`cmd` `` expansion).
   Premature and, as implemented in Claude Code, off-identity: it depends on
   a prompt-command layer Carina has not built, and its expansions run
   pre-loop and off the books. If adopted later, every template expansion is
   an execution that must pass the kernel policy check and be audit-recorded.
   The style-learning-from-git-log idea is noted for that future layer.
8. **Early input capture as a standalone near-term item.** Challenged as
   premature in isolation: polish for an input box that does not exist. Not
   discarded — folded into the TUI foundation cluster (P3.1) so it is
   designed in from the first frame rather than retrofitted, including the
   negative rule of never arming capture on promptless paths.
9. **Top-level side-effects-before-imports startup hack** (literal
   fire-then-await mechanism). A JavaScript module-load-order trick with no
   Go equivalent or need; adopting it literally would mean init side effects
   in package `init()`, an anti-pattern. The transferable principle — start
   expensive I/O early, join at the last responsible moment — survives as
   goroutine pipelining inside P1.8.

---

## 6. Open questions for the brand workstream

Decisions this plan defers to voice/tone/brand rather than settling by fiat.
Each has a placeholder default so implementation is never blocked.

1. **Governed-register voice.** How sober is sober? Default assumption:
   active voice, second person for user actions ("stopped by you"), rule ids
   always visible, no exclamation marks, no hedging. Does the brand want a
   named "kernel voice" that is distinct from the ambient product voice?
2. **The status glyph set.** P1.3 proposes a four-glyph vocabulary
   (ok / needs-auth / failed / ~degraded). Which exact glyphs, what are the
   ASCII/NO_COLOR fallbacks, and is `~` as the degrade mark a brandable
   asset (it already appears in degrade copy)?
3. **zh voice parity.** zh pools are first-class (§4.4), but are governed
   templates *translated* (en canonical, zh reviewed) or *dual-authored*?
   Who reviews zh governed strings, given the hard rule that governed copy is
   code-reviewed constants? Default: en canonical + reviewed zh, both frozen
   per release.
4. **Personality budget for Ambient.** How playful may spinners be in a
   product whose identity is trust? Proposed guardrail: playful in *waiting*
   states only; anything adjacent to a decision, denial, or audit is Governed
   register by definition. Needs a brand-approved banned-terms list for the
   §4.5 validator.
5. **Approval scope nouns.** "once / session / project" — are these the
   brand-approved names for grant scopes? They become audit-chain vocabulary
   (P1.1) and are then expensive to rename. Any future "organization" scope
   name should be reserved now.
6. **Audit narrative tone.** `audit replay`/`report` will render
   Governed-register summaries of chain events. Forensic-neutral
   ("patch applied; decision d_42, scope: session") vs narrative
   ("Alice approved…")? Default: forensic-neutral; names only with `--who`.
7. **Naming the degrade concept.** "Degraded" is engineer-accurate but
   cold. Is there a brand term for "running honestly at reduced capability"
   — a concept central enough to Carina's honesty posture to deserve one?
8. **Exit-code documentation as brand surface.** The P1.5 governance exit
   codes will be quoted in third-party CI configs forever. Does brand want a
   named, stable table ("Carina governance exit codes v1") in public docs?
9. **Microcopy refresh positioning.** `carina microcopy refresh` spends user
   tokens to widen personality pools under audit (§4.5). Is this marketed as
   a feature ("your Carina, your voice — on the record") or left as an
   undocumented power-user command until the brand voice is settled?
10. **TUI visual identity.** Border styles, agent badge shapes, diff palette
    for the P1.1/P3.1 surfaces — needs a terminal-safe palette spec
    (256-color + truecolor + mono fallbacks) from brand before P3.1 rendering
    work starts.

---

## Appendix A — Pattern → plan traceability

| Ranked pattern (source analysis) | Plan item |
|---|---|
| Kernel-backed approval surface (5-pattern merge) | P1.1 |
| Canonicalize-then-validate-then-decide pipeline | P1.2 |
| Failure-state and degrade taxonomy | P1.3 |
| Cascading interrupt as audited governance event | P1.4 |
| One engine, two renderers + NDJSON reframed | P1.5 |
| carina doctor three-state diagnostics | P1.6 |
| Microcopy engine (rescued buddy kernel) | P1.7, §4, P3.4 |
| Write-ahead user-turn persistence | P1.8 |
| Single-writer drain loop | P1.8 |
| Startup discipline (adapted fire-then-await) | P1.8 |
| Per-subcommand shell-AST permission decisions | P2.1 |
| Session forking with audit-anchored lineage | P2.2 |
| BYOK cost surface (inverted /cost) | P2.3 |
| BYOK credential precedence + auth status | P2.4 |
| Compaction as visible audited events + /context | P2.5 |
| Command metadata registry | P2.6 |
| Daemon-mediated typed agent coordination | P2.7 |
| Dirty-write protection | P2.8 |
| Tail-preserving truncation + persist-and-reference | P2.9 |
| Config-cascade warnings + audited migrations | P2.10 |
| Fail-open metrics vs fail-closed audit | P2.11 |
| TUI foundation cluster | P3.1 |
| Background task pill + two-axis state | P3.2 |
| Client-daemon reconnect state machine | P3.3 |

Rejected patterns: §5 (nine entries, three with rescued kernels).
