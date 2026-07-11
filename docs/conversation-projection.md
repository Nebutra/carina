# Conversation Projection Contract

Status: P0 protocol specification

Implementation status (2026-07-12): functionally closed. Projection `1.0.0`,
`session.review`, persistently signed `cp1` cursors, typed invalid/expired
cursor recovery, runtime negotiation, and typed Go/Python/TypeScript items and
review APIs are implemented. Additional process-kill scenarios remain
continuous hardening, not blockers for the projection contract.

## Purpose

This document defines the stable conversation model consumed by Carina clients.
It separates durable evidence from product presentation:

```text
audit/event log -> versioned reducer -> session items -> review/run/graph views
```

The event log is authoritative evidence. Session items are the authoritative
client-facing semantic projection. Higher-level views are deterministic
projections over items.

## Identity and ordering

Every projected item has:

- `session_id`;
- stable `item_id`;
- source event identity or source event range;
- monotonically ordered source position within the session;
- `projection_version`;
- timestamp for presentation, never for ordering;
- optional `task_id`, `turn_id`, `parent_id`, and `call_id`.

Ordering is by source position. Wall-clock timestamps may move backward and do
not affect reduction.

Item IDs must be deterministic for replay. Rebuilding the same projection
version from the same log produces byte-equivalent semantic items.

## Item families

The minimum stable families are:

- `thread.started`, `thread.updated`, `thread.completed`;
- `turn.started`, `turn.completed`, `turn.degraded`;
- `item.user_message`, `item.agent_message`;
- `item.tool_call.started`, `item.tool_call.completed`;
- `item.approval.requested`, `item.approval.resolved`;
- `item.question.requested`, `item.question.resolved`;
- `item.patch.proposed`, `item.patch.applied`, `item.patch.rolled_back`;
- `item.check.completed`;
- `item.compaction.completed`;
- `item.task.linked` for sub-agent, workflow, fork, and worker lineage;
- `item.artifact.created`;
- `item.policy.decision`;
- `item.runtime.changed`.

Unknown raw events are preserved in the audit log. A reducer may emit
`item.unknown` for diagnostic clients, but stable clients must not derive
business state from it.

## Reduction rules

1. Reduction is deterministic and side-effect free.
2. A reducer never reads current filesystem, configuration, network, model, or
   clock state.
3. Resolution items reference the request they close.
4. Duplicate raw events with the same event identity reduce once.
5. A terminal task state never transitions back to active within the same task
   identity; resume creates a new turn or attempt item.
6. Redaction happens before projection. Projection never restores secret text.
7. Large bodies are represented by artifact metadata and bounded preview.
8. Compaction creates lineage; it does not delete historical audit evidence.

## Cursor contract

`session.items` uses an opaque exclusive cursor.

The cursor binds to:

- session;
- projection version;
- log/projection epoch;
- last acknowledged source position.

The daemon creates a 32-byte HMAC key atomically in its local state directory
with mode `0600`; it survives daemon restarts. Cursor payloads are base64url
encoded but are not trusted until their HMAC, session, projection, epoch, and
position validate. The epoch derives from the retained projection boundary, so
normal append and restart preserve cursors while replacement or compaction of
that boundary expires them.

Responses contain `data` and `next_cursor`. Repeating a request with the same
cursor and unchanged source returns the same semantic page.

Malformed, tampered, or cross-session cursors return JSON-RPC `-32010` with
`data.code=invalid_cursor`. A cursor whose epoch is no longer retained returns
the same RPC code with `data.code=cursor_expired` and:

- current projection version;
- snapshot/restart hint;
- earliest available cursor when applicable.

Both errors include `snapshot_method=session.items`; clients discard the stale
cursor, fetch a fresh snapshot, and refresh `session.review`.

Silent clamping is forbidden for this API.

## Replay-then-tail

Lossless streaming follows this sequence:

1. Register an inactive bounded subscriber.
2. Read durable items strictly after the requested exclusive cursor.
3. Deliver replay items.
4. Atomically activate the subscriber.
5. Flush events buffered during replay.
6. Deduplicate overlap by stable event/item identity.
7. On overflow, disconnect the subscriber and require cursor catch-up.

The live transport is advisory. A client advances its durable checkpoint only
from an acknowledged replay/source cursor, not from receipt time alone.

## Review projection

`session.review` is a deterministic view with:

- session identity, current intent, success criteria, lifecycle state, and waiting reason;
- net diff grouped by patch transaction and file;
- commands and bounded outcomes;
- diagnostics, checks, and verification evidence;
- the final state of each logical approval and question;
- policy decisions and risk explanations;
- artifact references;
- rollback actions currently available;
- `projection_version` and `source_cursor`.

Items are reduced by logical `(type, item_id)` identity before categorization,
so lifecycle updates appear once in their latest state. A later turn supersedes
the lifecycle state and summary of an earlier turn. Tool calls to `run` are
commands; `goal_check` calls are checks.

Review data cannot authorize an action. Mutations still call explicit RPC
methods and pass through policy/kernel checks.

## Lease and execution attempt linkage

Remote execution items include `lease_generation`. Each successful claim
increments it. Renew and report requests must present the current generation.

The durable attempt identity is:

```text
(task_id, lease_generation)
```

Tool call identity is:

```text
(task_id, lease_generation, call_id)
```

A stale generation may publish neither lease renewal nor terminal outcome. Tool
side effects from a lost generation must be cancelled when possible and must
not be presented as the authoritative current result.

## Projection versioning

Additive optional fields do not require a new projection version. Changes to
item meaning, ordering, identity, terminal reduction, or cursor interpretation
do.

The daemon advertises supported versions during `runtime.initialize`. Clients
request one supported version or fail clearly. Persisted materializations carry
their reducer version and are discarded/rebuilt when incompatible.

## Client requirements

- Prefer session items and review projection over raw events.
- Persist cursors per session and projection version.
- Treat disconnect as stale state, not task failure.
- Reconnect with replay before accepting new live state as complete.
- Never infer permission from button visibility or cached review state.
- Render artifact preview as untrusted text and fetch full content explicitly.
- Preserve pending approvals/questions until a matching resolution item arrives.

## Conformance scenarios

All clients and SDKs must share fixtures for:

1. normal turn with tool call and completion;
2. approval request and resolution;
3. question request and resolution;
4. patch apply and rollback;
5. compaction event and continued turn;
6. sub-agent and workflow lineage;
7. replay/live overlap;
8. slow-consumer disconnect and catch-up;
9. cursor expiry and snapshot recovery;
10. stale worker generation after reassignment;
11. large output represented by artifact;
12. unknown future event compatibility.
