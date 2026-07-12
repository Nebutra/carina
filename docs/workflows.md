# Workflows

A workflow is a declarative multi-step agent pipeline: a dependency DAG of
steps, each delegated to an isolated, capability-attenuated subagent, with
every step audited and (in streaming mode) resumable. This document is a
usage guide — for the scheduling design rationale, see
[`docs/plans/2026-07-12-agent-swarm-dag-orchestration-design.md`](plans/2026-07-12-agent-swarm-dag-orchestration-design.md)
and [`docs/research/workflow-orchestration.md`](research/workflow-orchestration.md).
For the exact JSON shape, see
[`protocol/schemas/workflow-graph.schema.json`](../protocol/schemas/workflow-graph.schema.json).

## Writing a workflow

Workflows live as JSON files under `.carina/workflows/` — either in your
project (`<workspace>/.carina/workflows/`, takes precedence) or your home
directory (`~/.carina/workflows/`, shared across projects). The filename
doesn't matter; the `"name"` field is the identifier a run refers to.

The simplest shape (batch/BSP mode, the default — see
[`examples/workflows/review.json`](../examples/workflows/review.json)):

```json
{
  "name": "review",
  "description": "Scan changed files, review in parallel, then synthesize a report.",
  "steps": [
    {"id": "scan",   "agent": "scout",    "task": "List the files changed in the working tree."},
    {"id": "bugs",   "agent": "reviewer", "task": "Review for bugs:\n${scan}", "needs": ["scan"]},
    {"id": "perf",   "agent": "reviewer", "task": "Review for perf:\n${scan}", "needs": ["scan"]},
    {"id": "report", "agent": "writer",   "task": "Synthesize:\n${bugs}\n${perf}", "needs": ["bugs", "perf"]}
  ]
}
```

`${step_id}` interpolates a completed dependency's whole output into a later
step's `task` text. `needs` is the only required ordering constraint — steps
with no shared dependency run in parallel.

## Running a workflow

```bash
carina workflow run review                    # creates a safe-edit session in cwd, waits for completion
carina workflow run review "focus on auth"     # positional input, passed through to the run
carina workflow run review --session sess_123  # reuse an existing session instead of creating one
carina workflow run review --background        # returns as soon as the run is queued
carina workflow list                           # all runs, most recently updated first
carina workflow status <run_id>                # progress, per-step status, token/cost totals
carina workflow pause|resume|stop|restart <run_id>
```

`carina workflow run` without `--background` polls and prints live progress
(the same `completed/failed/skipped/total` shape a streaming run emits
internally — see [Observability](#observability) below) until the run
reaches a terminal state, then exits non-zero for anything other than
`completed` — scriptable the same way `carina run`/`ask` are.

Control commands operate on the live execution, not just its display record:

- `stop` cancels the run context shared by local subagents and remote dispatch
  waits, then leaves the durable run in `stopped`.
- `pause` stops admission of newly-ready nodes. Work already running is not
  suspended or killed and may finish while the run remains `paused`.
- `resume` releases nodes that became ready while paused. `restart` creates a
  new run ID and attempt from a terminal run.

Everything above is also reachable directly over RPC (`workflow.run`,
`workflow.list`, `workflow.detail`, `workflow.pause`/`resume`/`stop`/`restart`
— see [`protocol/jsonrpc/methods.json`](../protocol/jsonrpc/methods.json)) or
from inside a running agent via the `workflow` tool
(`{"tool":"workflow","workflow":"review","task":"..."}`).

## Streaming mode

Set `"execution_mode": "streaming"` to switch schedulers. The default (`""`
or `"bsp"`) runs steps in dependency *levels*: it collects every step whose
dependencies are satisfied, waits for **all** of them to finish, then
computes the next level — simple to reason about, but one slow step stalls
every sibling in its level, and a single step failure aborts the whole run.

Streaming mode dispatches a step the instant its own dependencies resolve,
independent of how long unrelated steps take, with a much higher step-count
ceiling (1000 vs. 64) and a different failure posture: a failing step
isolates to its own dependents by default (`"fail_fast": true` on a specific
step restores the old abort-everything behavior for something genuinely on
the critical path). Use streaming mode for anything with dozens of steps,
markedly uneven step durations, or where a peripheral failure shouldn't sink
an otherwise-successful run. See
[`examples/workflows/swarm-review.json`](../examples/workflows/swarm-review.json)
for a worked example using every field below.

### Structured input and conditional edges

```json
{"id": "bugs", "agent": "reviewer", "needs": ["scan"],
 "when": {">": [{"var": "scan.count"}, 0]},
 "input": {"files": "${scan.files}"},
 "task": "Review the given files for correctness bugs."}
```

- `input` resolves each value against a dependency's **JSON-parsed** output
  — `"${scan.files}"` becomes the real typed array from `scan`'s output
  (assuming `scan` finished with `{"files": [...], "count": N}`), not a
  string. It's appended to `task` as a labeled JSON block, additive on top
  of whole-string `${step_id}` interpolation, not a replacement for it.
- `when` is a small sandboxed boolean expression (`{"var":...}`, `==`, `!=`,
  `>`, `<`, `>=`, `and`, `or`, `not`) evaluated once the step's dependencies
  resolve. A falsy or malformed expression skips the step (fails closed) —
  its dependents are then evaluated the same way an upstream failure
  propagates, not stalled waiting on a step that will never run.

There is deliberately no way to write executable code (JS/Python/etc.) into
a condition or a graph definition — a workflow file is pure data, always.

### Dynamic graph generation

```json
{"id": "extra-reviewers", "agent": "reviewer", "needs": ["scan"], "kind": "generator",
 "task": "If scan.count > 20, use spawn_steps to add one reviewer per 20 files."}
```

A `"kind": "generator"` step's `done` summary may include a `spawn_steps`
envelope — new step objects, injected into the still-running graph:

```json
{"spawn_steps": [{"id": "extra_1", "agent": "reviewer", "task": "...", "needs": ["scan"]}],
 "rationale": "changeset is large enough to split"}
```

New steps may depend on existing or sibling (same-batch) steps; an existing
step can never be retroactively made to depend on a new one, so the graph
stays structurally acyclic without needing a runtime cycle check on every
injection. Bounded by a generation-depth ceiling and the run's total
step-count ceiling — a runaway generator hits a hard wall, not an
unbounded graph.

Generated definitions are journaled before the generator result is committed,
and every generated node has an implicit causal dependency on its generator.
After a daemon restart, the graph is restored before scheduling continues. An
identical generator replay is idempotent; reusing the same step ID with a
different definition hash fails closed. Dynamic nodes are added to
`workflow.detail`/`carina workflow status`, so totals and progress include the
graph that actually ran rather than only the static file.

### Live inter-step messaging (swarm channels)

`needs`/`input` only ever hand a dependent a *finished* step's output. To
receive updates from a step that is still **running**, declare
`"consumes_channel": ["progress"]` on the subscriber:

```json
{"id": "perf", "agent": "reviewer", "needs": ["scan"], "consumes_channel": ["review-progress"],
 "task": "Review for perf. Check review-progress for anything bugs finds while you work."}
```

Any streaming step — subscribed or not — can call the `swarm_publish` tool
(`{"tool":"swarm_publish","channel":"review-progress","payload":{...}}`); a
subscribed step calls `swarm_receive` (optionally with a `"channel"` field)
at any point during its own run to pull new messages since it last checked.
It's a poll, not a push — call it more than once if you want to notice
messages that arrive mid-run. Each channel retains at most 500 messages;
past that, the oldest are dropped and a slow subscriber is told how many it
missed rather than silently seeing fewer messages than expected.

### Remote dispatch

```json
{"id": "train", "agent": "unused-for-remote-steps", "needs": ["prepare"],
 "remote": true, "affinity": {"worker_pool": "gpu-heavy"},
 "task": "Run the GPU job described by:\n${prepare}"}
```

`"remote": true` (or a non-empty `"affinity"`, which implies it) routes the
step to an external worker process via the existing dispatch/lease/report
pipeline instead of an in-process subagent — `agent` is required by
validation but ignored; the dispatched task's `task` text runs verbatim
through whatever executor the worker is configured with (see
[`docs/worker-executor.md`](worker-executor.md)). `affinity.worker_pool`
requires a worker registered with that tag
([worker pool affinity](worker-executor.md#worker-pool-affinity)) — without
one, the step queues until a matching worker appears or the run's wait
times out. See
[`examples/workflows/remote-build.json`](../examples/workflows/remote-build.json).

Placing a task on the dispatch queue requires `Capability::RemoteDispatch`
approval — a stronger, separate gate from the same-process `SubagentSpawn`
every local step uses, since it hands execution to a process authenticated
only by a bearer credential, potentially on a different machine.

A remote step can still participate in [swarm channels](#live-inter-step-messaging-swarm-channels):
its executor result may include `channel_messages`, delivered as a batch
when the worker reports (not continuously while it runs — see
[worker-executor.md](worker-executor.md#publishing-to-a-swarm-channel)).
`consumes_channel` isn't available to a remote step itself (the external
executor has no in-process tool-dispatch loop to call `swarm_receive`
through), but a local step subscribed to the same channel receives whatever
the remote step published.

### Run-wide token budget

```json
{"token_budget": 200000}
```

An aggregate ceiling across every step's subagent token usage for the
**entire run** (0/omitted = unlimited) — distinct from the per-task budget
mechanism. Once aggregate spend meets the ceiling, not-yet-dispatched steps
are skipped with an audited reason (`"workflow token budget exhausted..."`);
a step already running is never killed — tokens are never refunded, so
"pause until headroom frees up" isn't meaningful the way it would be for a
renewable resource.

The current remote worker result schema does not carry token usage. A remote
step is therefore reported as **unmetered**, not as zero: rollups include
`unmetered_steps`, `budget_spent_is_complete: false`, and
`budget_enforcement: "observed_usage_only"`; CLI status prints the same
limitation. A `token_budget` can only enforce observed local usage until the
remote result contract gains metering.

## Observability

`carina workflow status <run_id>` (or `workflow.detail` over RPC) reports
per-step status plus aggregate `completed`/`failed`/`skipped`/`total` and
`progress` (fraction of steps that reached *any* terminal state, not just
`completed` — an isolated failure is still resolved, not stuck). A
streaming run also emits `workflow_progress_rollup` on its event stream
after every step resolves, carrying the same counts plus `budget_spent`
(and `budget_limit`/`budget_remaining` when `token_budget` is set) and
swarm-channel activity (`channel_messages_published`,
`channel_messages_evicted` when nonzero) — the aggregated view a large run
(hundreds of steps) is meant to be watched through, as opposed to
subscribing to every individual step's full event stream.

The operator detail is durable across daemon restarts. Step usage has an
explicit observation status, allowing clients to distinguish a measured zero
from unavailable remote metering.
