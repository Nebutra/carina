# Codex Phase B Absorption: Nebutra Risk Review

Source reviewed: `openai/codex` at `cca16a1`, focused on Guardian approval
review.

## Goal

Absorb Codex Guardian's useful philosophy without copying its product shape:
high-risk autonomous approvals should pass through a second, auditable point of
view before the agent executes the side effect.

Carina keeps the Rust kernel as the hard authority. Risk Review sits after a
kernel `requires_approval` decision and before autonomous approval. It can only
allow the existing approval flow to continue or block it; it never turns a
kernel denial into an allow.

## Modes

- `off`: no risk review.
- `advisory`: default. Review is recorded, but an agent approval may continue.
- `enforce`: a `deny` review blocks autonomous approval.

Interactive approvals and explicit operator `task.action.approve` stay human
controlled. Risk Review only governs the path where Carina would otherwise
auto-approve as `agent`.

## Reviewer Source

The default reviewer is deterministic and local. It derives risk from the
capability/resource and emits an assessment:

```json
{"outcome":"allow|deny","risk":"low|medium|high|critical","authorization":"unknown|low|medium|high","rationale":"..."}
```

If `CARINA_RISK_REVIEW_MODEL` / `risk_review_model` is configured, Carina may
use a model-backed reviewer with the same schema. Transport or parse failures
fail closed only in `enforce`; in `advisory` they are recorded and the existing
approval behavior continues.

## Audit

Each review is recorded as a `TaskCreated` lifecycle event with
`status=risk_review`, the target decision id, capability, resource, mode,
outcome, risk, authorization, source, and rationale. This keeps the hash chain
as the source of truth while allowing `session.items` to expose the review later.

## Later Work

The reviewer can later receive richer context from command parsed structure,
network trigger, MCP tool metadata, and turn item stream. This phase keeps the
scope small: gate auto-approval, record the assessment, and make the mode
configurable.
