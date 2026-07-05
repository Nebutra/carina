# Background Agent — Research & Absorption Analysis

> Research record for the "Background Agent" mechanism: what Claude Code ships,
> what the OSS/Python ecosystem does, what Carina already has, and a concrete
> plan to absorb it by generalizing existing primitives (not a rewrite).
> Companion to the four agent-mechanism docs (loop / goal / sub-agent / workflow).

## 1. What "Background Agent" is (and its philosophy)

Claude Code exposes **three** distinct execution shapes; only the first two are
"background". They are frequently conflated — keep them separate.

| Variant | Where it runs | Isolation | Monitor | Persistence | Purpose |
|---|---|---|---|---|---|
| **Background Agents** (local, `claude --bg`, "Agent View") | local machine, under a **supervisor** process | **git worktree** per run | `claude agents` dashboard; peek / attach / reply | JSONL transcript under `~/.claude/projects/…` | dispatch many *full* autonomous sessions, check back later |
| **Managed Agents** (SDK, cloud) | Anthropic-managed / self-hosted **sandbox** | per-session sandbox (Firecracker) | webhooks + SSE stream | server-side session history | long-running + **cron-scheduled** autonomous tasks |
| **Subagents** (*not background*) | inside the parent session's context | optional worktree (files only) | inline | parent context | delegate a side task, return a summary |

Carina already has the **subagent** shape (`spawnSubagent`). The **background**
shapes are new.

**Philosophy.** "Hand off, check back later." A background agent is a *full,
independent session* — its own context, tools, and file isolation — that runs
**detached** and is **supervised** so it survives the terminal closing, machine
sleep, and restarts. You drive a dashboard, glance at status, and step in only
when one needs you. The managed variant extends this to "build the agent once,
let infra run it on a schedule." The defining properties are therefore:
**durable + resumable + queryable + isolated + notify-on-done**, not merely
"async".

## 2. Technical principles (condensed)

- **Supervisor process** keeps detached sessions alive independent of any
  client; tracks state; persists metadata (`~/.claude/supervisor/`).
- **Event/transcript log as source of truth** — sessions are JSONL transcripts;
  state survives sleep/restart; resume = re-hydrate from the log.
- **Isolation via git worktrees** (`.claude/worktrees/<name>/`) so parallel runs
  never touch the same files; discard = drop the worktree/branch.
- **Attach / detach / resume** — reconnect to a running session, scroll back,
  reply, or leave it running.
- **Managed variant** adds: Firecracker microVM sandboxes, **server-side**
  session state, **cron deployments**, and **webhook/SSE** completion signals.

## 3. What Carina has today

Grounded in the code (`go/daemon`, `go/scheduler`, `go/worker`, `go/session-store`):

- **Async in-daemon execution** — `handleTaskSubmit` → `go d.runTask` (fire-and-forget goroutine); RPC returns immediately.
- **Cooperative cancel + status poll** — `task.status` / `task.cancel`; the loop checks `cancelled` each turn (`agent.go`).
- **Live event streaming** — `session.events.stream` via `Bus` (live-only, no replay-then-tail).
- **Session-level crash recovery** — `sessionstore` persists sessions + the kernel's append-only event log; `daemon.recover()` re-inits active sessions so they accept *new* work.
- **Workflow-step resume** — `workflowrun.go` persists each completed step; `runWorkflow` skips completed steps (the one real checkpoint/resume primitive we have).
- **Worker registry** — register/heartbeat/list; but workers **never execute** anything (no dispatch path); all execution is in-daemon local process.

## 4. Gap — what a real Background Agent still needs

| Capability | Carina status |
|---|---|
| Durable background runs surviving **daemon restart** | ❌ scheduler is in-memory; `recover()` doesn't resume tasks |
| Resume the **agent loop** (not just workflow steps) after crash | ❌ `runTask` transcript/turn state never checkpointed |
| Run **registry**: `task.list` / `task.result` / attach-with-replay | ❌ no list, `Task` has no result field, stream is live-only |
| **Notify-on-completion** (webhook / push) | ❌ none |
| **Remote / sandboxed** execution of the loop | ❌ workers are a registry only; no dispatch; no worktree/microVM isolation of a run |
| **Concurrency limits + panic isolation** | ❌ unbounded `go d.runTask`; no `recover()` guard → one panic kills the daemon |

**Verdict: it is genuinely a new feature** — but *additive*. Every gap closes by
**generalizing patterns already present** in `workflow.go` / `workflowrun.go` /
`session-store`, and by wiring the **already-written-but-unused** `scheduler.Next()`
and worker pool.

## 5. OSS references worth borrowing

| Project | Borrowable idea |
|---|---|
| **Daytona** (Go) | control-plane / runner / **in-sandbox daemon** split — the most directly applicable architecture, already in Go |
| **OpenHands SDK** | **event-sourced `ConversationState`** → pause/resume/replay/observability for free |
| **E2B / Firecracker / Fly Sprites** | microVM **memory+FS snapshots** → 5–30 ms resume; explicit Running/Paused/Snapshotting states, auto-pause-idle / auto-resume-on-call |
| **container-use** (Dagger) | **git-worktree-per-run + auto-commit** every step → isolation *and* audit trail from git itself |
| **GitHub Copilot / Cursor** | **PR-as-report-back**; hard guardrails (agent may only push to a reserved branch prefix; triggering user can't self-approve; human approval before CI) |
| **Cleanroom / microsandbox / Vercel Sandbox** | **deny-by-default egress + credential brokering** — the runtime, not the agent, holds secrets; real keys injected only at a verified TLS handshake to an allowlisted host |
| **SWE-ReX** | thin **`Deployment` abstraction** (local / container / remote) each starting an in-container session server the host drives |

## 6. Python durable-execution patterns (the "survive restart" layer)

Python frameworks bolt durability on via **Temporal / Restate / DBOS / Inngest /
Hatchet**. The convergent, language-agnostic lessons:

1. **Make the *step* the unit of durability, and memoize it.** Persist each
   completed step (esp. every LLM call and side-effecting tool call); re-inject
   on replay so a resumed run never re-pays for or re-fires completed work.
   (Carina already does this at *workflow-step* granularity — generalize to the
   *agent-loop turn*.)
2. **Externalize non-determinism** (clock, random, UUID, I/O) through the durable
   context so replay returns journaled values; use **idempotency keys =
   (run_id, step_id)** for real-world side-effects. This is what separates
   "checkpointing" from durable execution.
3. **Per-key single-writer state** (Restate *Virtual Objects*) — one durable
   instance per `session_id` serializes concurrent interactions, no external lock.
   In Go: a keyed goroutine-with-mailbox backed by the store.
4. **HITL / long waits as durable event-waits** (Hatchet evict-and-replay,
   Inngest `step.invoke`) — suspend, free the worker, re-hydrate on the event.
   Never block a worker on a human/approval/backoff.
5. **Versioning + replay-safety contract** — stamp each run with a code version;
   replay against that version; forbid nondeterministic reads on the replay path.
   (Redeploys otherwise corrupt in-flight runs — the #1 production failure.)
6. **Host-independent transcripts** — key the journal by run id (not cwd/host),
   in shared storage, so any node can resume it. Separate *conversation* state
   from *filesystem* state and checkpoint them independently.
7. **Plan history compaction early** — long runs' journals grow unbounded. (We
   already have `Transcript.compact`; extend the same idea to the durable journal.)

Closest architectural analogues to study: **Temporal** (deterministic
workflow/activity split; proven under OpenAI Codex & Replit; Go SDK), **Restate**
(Virtual Objects; Go SDK), **DBOS** (durability as a *library* on Postgres,
skip-completed-steps-on-replay). Carina is effectively building an in-house blend
of these three.

## 7. Recommendation — absorb, phased, reusing our primitives

Do it. It's new, high-value (it's the difference between "an agent you babysit"
and "an agent you dispatch"), and it composes cleanly with the capability kernel
+ audit log (which already give us the security guardrails the OSS projects
bolt on). Proposed phasing, highest-leverage first:

**Phase 1 — Durable local background runs (the core, ~all additive):**
1. Persist tasks + results: a `RunStore` modeled on `workflowrun.go`; add
   `Result/Summary/AppliedPatches/Mode` to `scheduler.Task`, persist on every
   `SetStatus`. → unlocks `task.list` / `task.result`.
2. Checkpoint the agent-loop transcript after each turn (mirror
   `runWorkflow`'s per-step snapshot); extend `daemon.recover()` to relaunch
   `running` tasks from the checkpoint. → **restart survival**.
3. Replace unbounded `go d.runTask` with a **drain loop** over the
   already-written `scheduler.Next()` + a concurrency semaphore, each run wrapped
   in `defer recover()`. → **backpressure + panic isolation**.
4. `task.attach` stream = replay-from-cursor then follow-live (add a monotonic
   `seq` in `record()`). → **walk-away / lossless reconnect**.
5. Completion hook (`notify:{webhook_url}`) in `finish`/`degrade`, egress gated by
   the kernel + redacted. → **notify-on-done**.

**Phase 2 — Isolation & report-back:** git-worktree-per-run (container-use
pattern) so parallel background runs don't collide; optional PR-as-report-back
with reserved-branch guardrails (Copilot pattern).

**Phase 3 — Remote/sandboxed execution:** finally wire the worker pool as a real
`Deployment`/dispatch seam (SWE-ReX pattern); sandbox/microVM runs (E2B pattern);
deny-by-default egress + credential brokering (Cleanroom pattern) — most of which
our capability kernel + secret broker already anticipate.

Phase 1 is the "Background Agent" MVP and is genuinely additive — no rewrite,
mostly generalizing `workflow.go`/`workflowrun.go` and turning on `scheduler.Next()`.

## Sources

Claude Code Agent View / worktrees / subagents: code.claude.com/docs · Managed
Agents + scheduled deployments: platform.claude.com/docs/en/managed-agents ·
Daytona (github.com/daytonaio/daytona) · OpenHands
(github.com/OpenHands/software-agent-sdk) · E2B (github.com/e2b-dev/E2B) ·
container-use (github.com/dagger/container-use) · SWE-ReX
(github.com/SWE-agent/SWE-ReX) · Buildkite Cleanroom
(github.com/buildkite/cleanroom) · Temporal (temporal.io) · Restate
(restate.dev) · DBOS (github.com/dbos-inc/dbos-transact-py) · Inngest
(inngest.com) · Hatchet (github.com/hatchet-dev/hatchet) · LangGraph interrupts ·
Claude Agent SDK Python (github.com/anthropics/claude-agent-sdk-python).
