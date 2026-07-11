# OpenHands Source Analysis and Carina Absorption Plan

Date: 2026-07-12

Upstream snapshots reviewed:

- `OpenHands/OpenHands@3949e1cc17d9443f1f4ef7d34d428baf065cd919`
- `OpenHands/software-agent-sdk@cf6c2a3a4ace65b651ea29032ab6b4a74d7bb41a`
- `OpenHands/agent-canvas@90223f99f43a977ec454c0fc15e93642b7053075`

## Executive conclusion

OpenHands is no longer one coherent application repository. Its current design
is a three-part product:

1. Agent Canvas owns the multi-backend operator UX and automations.
2. Agent Server owns conversations, event delivery, lifecycle, leases, remote
   access, profiles, hooks, skills, terminals, files, and workspaces.
3. Software Agent SDK owns the agent loop, typed events, conversation state,
   event persistence, context condensation, tools, plugins, and local/remote
   parity.

The best lesson is not a particular ReAct prompt or tool implementation. It is
the decision to make a durable, event-sourced `Conversation` the stable product
object and to let local and remote execution implement the same contract.

Carina should absorb that product model, but not OpenHands' implementation
shape. Carina already has a stronger capability kernel, policy model,
transactional patch lifecycle, audit chain, secret boundary, and protocol-first
control plane. Replacing those with OpenHands' Python service graph would be a
regression.

The highest-value absorption is therefore:

- promote Carina's normalized item/event stream into the canonical replayable
  conversation projection;
- enforce one active writer per session with a generation-fenced lease;
- specify lossless replay-then-tail pagination and reconnect semantics;
- make runtime/backend/workspace selection a first-class product concept;
- add OpenHands-grade concurrency, reconnect, slow-consumer, lease-contention,
  and high-output stress tests;
- rebuild the web and VS Code surfaces around a shared operator information
  architecture instead of raw event inspection.

## How OpenHands works

### 1. Event-sourced conversation

`ConversationState` is the durable state projection. `EventLog` is the ordered
history. User messages, model actions, tool results, state changes, security
decisions, condensation, delegation, and lifecycle changes are typed events.

The effective loop is:

```text
input event
  -> append to EventLog
  -> reduce into ConversationState
  -> agent selects next action
  -> tool/security/runtime produces result event
  -> append and reduce
  -> continue, pause, finish, or condense
```

This gives pause/resume, fork, replay, remote observation, and debugging one
common substrate. OpenHands does not need a separate bespoke representation for
each UI because clients consume the event stream and derived state.

Carina already has most of the raw ingredients: audit events, session events,
task checkpoints, `session.attach`, cursors, and normalized `session.items`.
The missing piece is declaring one canonical replay contract and treating all
other views as projections of it.

### 2. Single writer and generation-fenced lease

Agent Server uses a per-conversation lease with a generation number. The owner
renews the lease and wraps state-changing operations in guarded writes. A stale
owner cannot resume writing after another process has taken ownership.

This is more than a mutex:

- a mutex protects threads in one process;
- a TTL lease detects dead owners;
- a generation fence prevents a delayed former owner from committing writes;
- a conversation-scoped FIFO lock preserves deterministic input ordering.

This is directly relevant to Carina's daemon restart, remote worker dispatch,
reconnect, task steering, and future clustered control plane. A plain task
status or worker heartbeat is insufficient to prevent split-brain execution.

### 3. Replayable, pageable event delivery

Agent Server reads event pages directly from the event log, resolves a stable
`page_id` to an index, and returns a next page identifier. WebSocket delivery is
the live transport, not the source of truth. The test suite covers reconnect
storms, event loss, slow consumers, high-volume shell output, and concurrent
conversations.

The important invariant is:

```text
durable log cursor + live subscription handoff = no gaps and no duplicates
```

Carina's `since` cursor is directionally correct. It should be formalized as an
opaque durable cursor with snapshot-boundary semantics, monotonic ordering, and
explicit compaction behavior.

### 4. Local/remote parity

The SDK exposes local and remote conversation implementations behind the same
base contract. Agent Canvas can connect to several Agent Servers and switch
between local, Docker, VM, cloud, or organization infrastructure.

This is good product architecture because execution placement is not smeared
through the chat UI. It is a backend/workspace choice. Carina's daemon, worker,
Gateway, SDK, and Nebutra boundary can express a stronger version of the same
idea, but the concept is not yet equally visible in the UX.

### 5. Context condensation

OpenHands uses rolling condensers and an LLM summarizing condenser. The
algorithm keeps a recent working suffix, summarizes older history, and emits
condensation as conversation state rather than silently mutating an in-memory
prompt.

The useful principle is observable context transformation. A user or debugger
can know when history was condensed and which summary became authoritative.
Carina should retain its frozen prompt/memory boundaries and budget controls,
while recording compaction lineage in the item stream.

### 6. Extensibility

OpenHands separates tools, skills, plugins, hooks, agent profiles, MCP, and
workspace implementations. Profiles are user-facing launch configurations;
plugins and tools are runtime capabilities; hooks are lifecycle interception;
skills are contextual instructions.

This taxonomy is clearer than treating every extension as a tool. Carina has
most mechanisms already, but discovery and UX should present these as distinct
objects with distinct trust and lifecycle rules.

### 7. Goal loop and delegation

OpenHands implements a goal controller and judge loop, plus delegation events
and sub-conversations. The valuable pattern is that a delegated run remains a
normal conversation with lineage, status, and events, rather than an opaque
function call returning text.

Carina's attenuated sub-agent permissions are stronger. It should keep those
and add clearer parent/child conversation lineage, independent budgets, and a
task graph projection.

## The essence worth taking

| OpenHands capability | Why it matters | Carina adaptation |
|---|---|---|
| Event-sourced `Conversation` | One model for replay, pause, fork, UI, and remote clients | Make `session.items` the canonical projection over durable events |
| Generation-fenced lease | Prevents split-brain writers across restart or workers | Add session execution lease to scheduler/worker dispatch |
| Pageable event log | Reliable reconnect and bounded history reads | Specify opaque cursors, replay-then-tail, retention, and compaction |
| Local/remote contract parity | Execution placement becomes replaceable | Unify daemon/local-worker/remote-worker behind a runtime endpoint descriptor |
| Backend switcher UX | Users understand where an agent runs | Surface endpoint, workspace, policy, trust, and health in all clients |
| Observable condensation | Long runs remain debuggable | Emit compaction items with source range, summary hash, token delta, and reason |
| Profiles/skills/hooks/plugins taxonomy | Better discovery and trust reasoning | Build a common registry model but preserve kernel capability checks |
| Stress-test suite | Finds real event-loop and distributed failures | Port scenarios, not Python fixtures |
| Conversation lineage | Delegation remains inspectable | Project parent/child/fork/workflow edges into one task graph |

## The parts to reject

### 1. Service and injector sprawl

OpenHands has accumulated a large Python graph of services, injectors, routers,
models, compatibility paths, and app-specific adapters. It enables extension,
but makes ownership and invariants difficult to follow. Some files are several
thousand lines long and mix lifecycle orchestration, persistence, git behavior,
security enrichment, and product policy.

Carina should keep explicit Go interfaces and small constructors. Do not import
a general dependency-injection framework. Registration should be declarative;
critical lifecycle paths should remain directly traceable.

### 2. Runtime security as an optional analyzer

OpenHands can attach a security analyzer to actions, but its default local mode
can run directly on the host with broad filesystem access. Analysis is not
authority.

Carina's kernel decision must remain the authority for every side effect.
LLM-based security analysis may enrich risk, explain a decision, or request a
tighter profile, but must never grant a capability or bypass canonical policy.

### 3. Python object serialization as the protocol center

Pydantic discriminated unions are effective inside one Python ecosystem, but
Carina supports Go, Rust, Zig, TypeScript, Python, CLI, TUI, web, VS Code, and
remote workers. JSON Schema and protocol conformance must stay authoritative.

### 4. Product migration churn leaking into architecture

The top-level OpenHands repository currently points to separately evolving
Agent Canvas and SDK repositories. That separation may become clean, but the
transition produces duplicated concepts and compatibility layers.

Carina should define stable ownership now: local execution authority in Carina,
identity and synchronization in Nebutra Cloud, clients as protocol consumers.

### 5. A broad UI without a governance-first review model

Agent Canvas is richer than Carina's web UI, but Carina should not clone a chat
application. Its differentiation is trustworthy action review. Diff provenance,
policy rationale, effective capability, affected resources, rollback, and audit
verification must be first-class, not hidden behind a generic event timeline.

## Carina gap assessment

### Stronger today

- Kernel-enforced capabilities and permission profiles.
- Transactional propose/apply/verify/rollback patch lifecycle.
- Tamper-evident hash-chained audit trail.
- Canonicalize/validate/decide execution pipeline.
- Secret broker and explicit egress boundary.
- Sub-agent permission attenuation.
- Cross-language, schema-first protocol discipline.
- Clear local authority versus Nebutra Cloud boundary.

### Equivalent or close

- Durable sessions and background tasks.
- Attach/replay and event streaming.
- Pause, cancel, steering, structured questions, and approvals.
- Agent profiles, skills/commands, plugins, MCP, workflows, and memory.
- Local and remote worker substrate.
- Context condensation and cost/budget accounting.

### Behind

- No formally documented canonical conversation reduction contract.
- No obvious generation-fenced single-writer lease around session execution.
- Event cursor retention, compaction, handoff, and deduplication semantics are
  not yet a public protocol contract.
- Remote worker placement is less mature and less visible to users.
- Web and VS Code clients are functional operator shells, not complete review
  workspaces.
- Stress coverage is not yet organized around distributed/event-stream failure
  modes at OpenHands' breadth.
- Extension taxonomy and discovery are implemented more deeply than they are
  explained or navigated in the product.

## UX and DX target

### Operator information architecture

Carina should use five stable views across TUI, web, and VS Code:

1. **Inbox**: approvals, questions, conflicts, policy blocks, failed checks.
2. **Runs**: working, queued, paused, completed, degraded, with endpoint and
   workspace identity visible.
3. **Review**: net diff, files, commands, diagnostics, test evidence, policy
   decisions, rollback action.
4. **Graph**: parent run, sub-agents, workflow steps, forks, workers, budgets.
5. **System**: daemon/endpoints, workers, providers, profiles, plugins, MCP,
   context engine, egress, and audit health.

Chat/transcript remains inside a run. It is not the top-level product model.

### Run header

Every client should show the same compact header:

```text
title | state | workspace | runtime endpoint | model | policy profile
cost/budget | changed files | checks | waiting reason | audit health
```

This eliminates the current need to infer authority and placement from raw
events or settings dialogs.

### Review object

The primary action should be reviewing a proposed outcome, not scrolling a
transcript. A review projection should contain:

- intent and success criteria;
- net diff grouped by file and transaction;
- commands executed and exit outcomes;
- diagnostics and verification evidence;
- capabilities requested, denied, approved, or attenuated;
- secrets and network hosts referenced by handle/host metadata only;
- unresolved questions and conflicts;
- commit/export/rollback actions permitted by current policy.

### SDK developer experience

Provide one typed flow in every SDK:

```text
create or attach conversation
  -> subscribe from cursor
  -> reduce typed items
  -> answer/approve/steer/cancel
  -> await terminal outcome
  -> fetch review projection and audit proof
```

Add generated examples and conformance tests that execute the same scenario in
Go, Python, and TypeScript. Generic JSON-RPC remains an escape hatch, not the
primary documentation path.

## Implementation plan

### P0: protocol invariants

Implementation status (2026-07-12): functionally closed. `session.review`, projection `1.0.0`
negotiation, session-bound `items:v2` cursors, typed invalid/expired cursor
messages, worker lease generation fencing, and typed Go/Python/TypeScript review
wrappers are implemented. Existing replay/live handoff, slow-consumer,
lease-reassignment, restart, SDK, and operator-surface gates pass. Broader
generated fixtures and fault injection are continuous reliability investment,
not unclosed OpenHands absorption requirements.

1. Write a `Conversation Projection` specification defining event ordering,
   item reduction, terminal states, fork/parent lineage, and replay behavior.
2. Add `execution_owner`, `lease_generation`, `lease_expires_at`, and guarded
   commit semantics to active session/task execution.
3. Specify cursor guarantees: opaque token, inclusive/exclusive behavior,
   replay/live handoff, deduplication key, expiry, compaction response, and
   snapshot recovery.
4. Add a read-optimized `session.review` projection generated from canonical
   items, not independently stored UI state.

Acceptance: kill and restart the daemon during model call, command execution,
approval wait, event handoff, and worker reassignment without duplicate side
effects or missing visible items.

### P1: UX and distributed reliability

1. Replace the web raw-event inspector with Inbox, Runs, Review, Graph, System.
2. Upgrade VS Code from tree plus generated Markdown into a webview-based review
   panel using the same projection and action contracts.
3. Add runtime endpoint descriptors and make placement visible/selectable.
4. Port stress scenarios: reconnect storm, slow subscriber, high-volume output,
   concurrent conversations, parallel sub-agents, lease contention, webhook
   slowness, event-loop responsiveness, and long-running commands.
5. Add bounded output artifact references so event streams carry metadata and
   previews rather than arbitrarily large command bodies.

Acceptance: clients recover from forced disconnects and render the same final
review without full reload, duplicate actions, or unbounded memory growth.

### P2: ecosystem polish

1. Present profiles, skills, hooks, plugins, MCP servers, workers, and context
   engines through a shared registry/status API with distinct trust semantics.
2. Record compaction lineage and expose a context-inspector view.
3. Make sub-agent and workflow lineage navigable as one graph with independent
   budgets and effective permission deltas.
4. Publish end-to-end SDK recipes and generated protocol compatibility tables.
5. Add migration/version gates for persisted events, projections, registries,
   and worker/runtime descriptors.

## Architectural rules for absorption

1. Borrow invariants and failure tests before borrowing abstractions.
2. The durable event log is the source; WebSocket and UI stores are caches.
3. A model-generated security opinion can only tighten or annotate policy.
4. Every active session has exactly one fenced execution owner.
5. Every side effect has an idempotency key and an audit event before result
   publication.
6. UI projections are reproducible from canonical events and versioned reducers.
7. Large payloads are artifacts referenced by hash, not event-stream bodies.
8. Local, worker, and remote execution share contracts but not credentials.
9. Extension discovery never implies authority; the kernel remains decisive.
10. Do not add a framework when a small explicit interface preserves the same
    invariant.

## Recommended immediate decision

Start with P0, not a front-end rewrite. The most valuable OpenHands capability
is trustworthy conversation continuity under concurrency and remote execution.
Once the projection, lease, and cursor contracts are explicit, Carina can build
a substantially better governance-first UX without coupling clients to daemon
internals or repeating OpenHands' service-layer complexity.

## Closure record

Closed on 2026-07-12. The event/review projection, version negotiation,
session-bound cursors, replay/live recovery, slow-consumer behavior, artifact
transport, generation-fenced worker leases, typed SDK parity, real-daemon
conformance, Web operator information architecture, and VS Code native review
panel are implemented and verified. This document has no remaining execution
TODO. New cluster, retention, or visualization work requires a new design with
its own evidence and acceptance criteria.
