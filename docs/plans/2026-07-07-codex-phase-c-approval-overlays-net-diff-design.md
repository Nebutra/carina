# Codex Phase C Absorption: Approval Overlays and Turn Net Diff

Source reviewed: `openai/codex` at `cca16a1`, focused on the deferred
execpolicy overlay and turn net-diff ideas.

## Goal

Absorb the remaining useful Codex philosophy without changing Carina's authority
model:

1. Approval reuse should be explicit, explainable, and auditable.
2. A turn should expose its net code changes as a stable derived view.

The Rust capability kernel remains the hard authority. These mechanisms can
only explain or summarize decisions already represented by kernel events.

## C1: Approval Overlays

Carina already has session approval memory, but it is implicit:
`capability + coarse resource prefix` and no durable rationale. Phase C turns
that into a first-class session overlay.

An approval overlay records:

- capability;
- resource prefix;
- source decision id;
- approver;
- justification;
- creation timestamp.

When a later request evaluates to `requires_approval`, the kernel may satisfy it
from a matching overlay. A `denied` decision is never rescued. Overlay matches
must be audited with the overlay id, source decision id, approver, and
justification, so the behavior is visible rather than a silent cache hit.

The Go API keeps backwards compatibility by allowing `ApproveForSession` without
a justification, but all Carina-owned autonomous/session approvals should supply
a concrete reason.

## C2: Turn Net Diff

Carina should not add a second file watcher or infer changes by scanning the
workspace. The hash-chained audit log remains the source of truth.

`session.items` should derive turn diff items by correlating:

- `PatchProposed`: patch id, affected files, reason;
- `PatchApplied`: patch id, new hash, rollback pointer;
- `PatchFailed`: patch id and error;
- `RollbackCompleted`: patch id.

For each task/turn, the projection emits a `turn_net_diff` item summarizing the
patches that changed the workspace in that turn. Rolled-back patches are shown
as reverted, not as active net changes. The projection is derived and
non-authoritative, like the rest of `session.items`.

## Testing

Approval overlays need kernel-level tests for matching, deny non-rescue, audit
payload, and Go wrapper compatibility. Daemon tests should prove
session-approved requests include justifications.

Turn net diff needs pure projection tests using synthetic audit events, plus at
least one daemon/kernel integration path if the existing test helpers can apply
patches without excessive setup.

## Sequencing

Implement as two commits:

1. `feat: add approval overlays with justifications`
2. `feat: expose turn net diff items`

This keeps permission semantics separate from UX projection.
