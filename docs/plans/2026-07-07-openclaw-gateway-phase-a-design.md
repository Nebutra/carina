# OpenClaw Gateway Absorption Phase A: RPC Method Catalog

Source review: `docs/research/openclaw-gateway-source-review.md`.

## Decision

Absorb OpenClaw Gateway's descriptor-first control-plane philosophy before
adding any new HTTP or WebSocket Gateway surface.

This phase does not port OpenClaw's TypeScript Gateway. It makes Carina's
existing JSON-RPC daemon behave like a classified Gateway core:

- every daemon RPC method has a machine-readable descriptor;
- descriptors carry method name, least scope, remote exposure, stream status,
  discovery visibility, and control-plane-write metadata;
- remote exposure is derived from the same descriptor table instead of a
  separate string allowlist;
- daemon strict mode refuses registered handlers that lack descriptors;
- `gateway.methods` exposes the live method catalog for CLI/UI/future WS
  `hello-ok` discovery.
- `carina gateway methods` gives operators a first CLI surface for inspecting
  the same live catalog.

## Scope Model

Initial scopes are intentionally small:

- `read`: read-only status, list, replay, catalog, audit, and result methods;
- `write`: mutating session/task/workspace actions inside the local operator
  boundary;
- `admin`: high-risk control-plane or secret/config/policy actions;
- `worker`: remote worker lease protocol;
- `stream`: long-lived event subscriptions.

These scopes are metadata and transport policy today. They are the foundation
for a later role/scoped Gateway handshake.

## Non-goals

- No OpenAI-compatible `/v1/*` endpoint in this phase.
- No `/tools/invoke` HTTP endpoint in this phase.
- No mobile/device pairing in this phase.
- No user-facing auth token changes in this phase.

Those should build on this catalog rather than bypass it.

## Testing

- Unit-test strict descriptor enforcement in `go/rpc`.
- Unit-test descriptor-derived remote exposure in `go/rpc`.
- Extend daemon handler coverage to call `gateway.methods` and assert core
  descriptors such as `daemon.status` and `task.submit`.
- Compile and help-check the CLI entrypoint.
