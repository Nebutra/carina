# OpenHands Absorption Trade-offs

Status: accepted for P0 implementation

Closure status (2026-07-12): complete. No deferred item in this ADR remains an
OpenHands absorption TODO. Dedicated cluster graph, projection materialization,
cursor epochs, and a standalone context inspector require new evidence
(clustered deployment, retained-log compaction, or measured projection cost)
and must be proposed as new scoped work rather than carried as perpetual debt.

## Decision summary

Carina will absorb OpenHands' durable conversation invariants without adopting
its Python service topology or chat-first product shape.

The decisions are:

1. The append-only audit/event record remains authoritative.
2. `session.items` becomes the stable client-facing conversation projection.
3. Raw events remain available for audit, recovery, and compatibility, but new
   UX must not infer product state by scanning ad hoc payload fields.
4. Remote execution remains at-least-once. Every lease carries a monotonically
   increasing generation fencing token.
5. Review state is a reproducible projection, not independently mutable UI data.
6. Large outputs live in the content-addressed artifact store and events carry
   bounded previews plus hashes.
7. TUI, web, VS Code, and SDKs share protocol semantics, not UI implementation.

## Trade-off 1: raw events versus normalized items

### Options

**A. Raw events only**

Lowest daemon complexity, but every client reimplements reduction logic. This
already causes the web and VS Code integrations to scan payloads for approval,
question, diff, and status fields. Client behavior drifts as event producers
evolve.

**B. Normalized items only**

Clean client contract, but loses low-level audit fidelity and makes recovery or
forensics depend on a potentially lossy projection.

**C. Authoritative raw log plus versioned normalized projection**

Chosen. The raw log is the durable evidence. A versioned reducer produces
stable items for products and SDKs. Reducer changes require compatibility tests
and, when semantics change, a new projection version.

### Consequences

- `session.attach` remains a compatibility/raw-event API.
- `session.items` is the preferred application API.
- Clients must stop parsing arbitrary raw payloads for new features.
- Projection version is included in runtime capabilities and review responses.

## Trade-off 2: integer positions versus opaque cursors

### Options

**A. Public integer offsets**

Easy to implement and debug, but exposes storage layout and cannot represent
compaction epochs, reducer versions, or snapshot recovery.

**B. Event IDs as cursors**

Stable across insertion-free logs, but requires an index and still needs an
explicit answer for deleted or compacted prefixes.

**C. Opaque signed cursor**

Chosen for normalized items and future review pagination. It can encode stream,
projection version, epoch, and position while allowing storage changes.
Raw `session.attach` integer offsets are a permanent compatibility and audit
surface; application clients use signed projection cursors.

### Required behavior

- Cursor is exclusive: results start after the acknowledged position.
- Reusing a cursor is idempotent.
- Unknown, malformed, or cross-session cursors fail closed.
- Expired/compacted cursors return a typed `cursor_expired` error plus a
  snapshot recovery hint; they are never silently clamped.
- Live handoff registers the subscriber before replay and deduplicates overlap.

## Trade-off 3: exactly-once versus at-least-once execution

Exactly-once remote execution is not achievable across process and network
failure without moving every external side effect into one transactional
system. Claiming it would be misleading.

Carina chooses at-least-once task delivery with effectively-once guarded side
effects:

- lease generation fences stale executors;
- `(task_id, lease_generation, call_id)` identifies an execution attempt;
- side-effect tools use stable idempotency keys where the target supports them;
- the capability/audit boundary records intent before result publication;
- stale workers cannot renew or publish terminal results;
- non-idempotent operations require transactional Carina primitives or human
  review rather than blind retry.

The generation token is necessary even when `worker_id` matches. A restarted
worker or an abandoned execution branch can reuse the same identity.

## Trade-off 4: lease generation source

### Options

**A. Random UUID per lease**

Unforgeable enough but not orderable. Operators cannot immediately tell which
lease is newer.

**B. Separate persistent monotonic counter**

Semantically clean but adds another persisted field and migration.

**C. Dispatch attempt number as generation**

Chosen now. Attempts already increase on every successful claim and persist
with the task. `lease_generation == attempts` is simple and orderable.

If retry accounting later diverges from ownership epochs, generation must split
into its own monotonic counter without changing the protocol meaning.

## Trade-off 5: review projection storage

### Options

**A. Store a mutable review document**

Fast reads but risks divergence from audit events and requires transactional
updates across every action path.

**B. Recompute the complete review on every request**

Always correct but increasingly expensive for marathon sessions.

**C. Versioned materialized projection with deterministic rebuild**

Chosen. The daemon may cache/materialize review state, but it must be rebuildable
from canonical events/items and carry the reducer version and source cursor.

The first implementation can recompute on demand. Materialization is justified
only after profiling.

## Trade-off 6: security analyzer versus capability authority

OpenHands permits a security analyzer in the action path. Carina will support
model- or rule-based risk enrichment only as a tightening and explanation
signal.

It cannot:

- grant a denied capability;
- weaken a permission profile;
- bypass path, command, egress, secret, patch, or plugin policy;
- alter the canonical resource being authorized.

The Rust kernel remains the sole authority.

## Trade-off 7: UX center of gravity

Agent Canvas is conversation-centric. Carina will be outcome-and-governance
centric because that is the product advantage.

The common information architecture is:

- Inbox: actionable approvals, questions, conflicts, and failures.
- Runs: queue and lifecycle across endpoints.
- Review: diff, commands, checks, policy, artifacts, and rollback.
- Graph: delegation, workflows, forks, workers, and budgets.
- System: endpoint, provider, policy, extension, and audit health.

Transcript is a run detail, not the global home screen.

## Trade-off 8: shared frontend versus shared contract

A single cross-platform frontend would reduce visual divergence but would harm
terminal ergonomics and VS Code-native workflows. Carina chooses separate native
surfaces sharing:

- generated protocol types;
- projection/reducer fixtures;
- microcopy vocabulary;
- state and action semantics;
- accessibility and reconnect acceptance scenarios.

Pixel parity is not a goal. Behavioral parity is.

## Rejected absorptions

- General dependency-injection container.
- Python/Pydantic objects as the cross-language protocol authority.
- Optional host-wide execution as the easy default.
- Client-side security approval without kernel authority.
- Unbounded event payloads for terminal output.
- UI state inferred from arbitrary raw event payload scanning.
- A front-end rewrite before projection and cursor contracts stabilize.

## Verification gates

P0 is accepted only when all hold:

1. Same-worker stale lease generation cannot renew or report.
2. Reconnect during replay/live handoff loses and duplicates zero durable events.
3. Slow consumers cannot block publishers and can recover from a cursor.
4. Artifact integrity and session scoping are enforced.
5. SDK conformance fixtures agree across Go, Python, and TypeScript.
6. Review projection fixtures rebuild deterministically from canonical items.
7. Existing raw-event consumers retain a documented migration path.
