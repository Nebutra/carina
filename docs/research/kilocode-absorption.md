# KiloCode Absorption Decision

Source reviewed: `kilo-org/kilocode` at
`619b595f4b2b2853b351bf081d8f0d378cdd78a7`.

Carina absorbs mechanisms only when they preserve the daemon, capability
kernel, and append-only audit log as the single authority.

## Absorbed

- Additive, typed tool-call lifecycle events with stable call IDs.
- Runtime stage events projected from authoritative audit events rather than
  inferred from chat rendering.
- Content-addressed tool-output artifacts with session/task/call scope,
  bounded reads, integrity verification, TTL support, and storage quotas.
- Actionable error and retry contracts that clients can interpret without
  parsing display text.
- Real authoritative event streaming in every SDK, including independent
  subscriptions, bounded queues, cancellation, and explicit unsubscribe.
- Fail-closed daemon/kernel event-schema negotiation and write-before-publish
  ordering for authoritative events.

Legacy command, patch, and approval events remain during the compatibility
window. `session.items` prefers the new lifecycle when both representations are
present and retains the old projection for historical logs.

Provider retry governance is deliberately process-local. Each daemon applies a
provider-keyed sliding-window circuit breaker and token-bucket retry budget,
bounded again by the per-request attempt and elapsed-time policy. Scheduler
pressure can pause retry admission; sleeping retries do not retain admission
permits. Runtime capabilities report the scope as `daemon`, so this mechanism
must not be interpreted as a distributed circuit breaker or global budget.

Raw audit persistence remains invariant across client views. `session.attach`
and `session.events.stream` default to `compat`; clients may request
`canonical`, which hides only the duplicate `ToolRequested`, `ToolApproved`,
and `ToolDenied` lifecycle records after persistence. Command, patch, policy,
and capability events remain visible because they are authoritative audit
facts. Cursors always advance over raw audit positions, including filtered
records.

## Deliberately Rejected

- Tool status inferred from display strings.
- Raw tool output or local artifact paths in immutable audit events.
- UI-derived task state or stringly typed cross-client event buses.
- XML/text injection of subagent results into parent context.
- In-process high-privilege plugins and unbounded MCP startup concurrency.
- Model-name substring routing and permissive fuzzy edits.
- Silent schema degradation when daemon and kernel versions disagree.

## Trade-offs

The lifecycle and artifact contracts add storage, event, and adapter overhead.
In return, failures, retries, approvals, cancellation, and output access become
replayable and testable across CLI, TUI, IDE, web, and SDK clients. Artifact
payloads remain local-only in this version; remote exposure requires a separate
authorization and retention design.

The first implementation wraps native tools and the existing governed MCP,
patch, memory, delegation, and code-intelligence paths without deleting legacy
events. Removal of legacy emission is a protocol-major decision, not cleanup
for this change.

Cross-daemon retry budgets and provider circuits remain deferred until Carina
has a consistency service with leases and fencing. A local file or best-effort
broadcast must not be presented as distributed retry governance.
