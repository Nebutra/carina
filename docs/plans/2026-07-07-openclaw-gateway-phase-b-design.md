# OpenClaw Gateway Absorption Phase B: Handshake and Dynamic Scopes

Source review: `docs/research/openclaw-gateway-source-review.md`.
Builds on Phase A: `docs/plans/2026-07-07-openclaw-gateway-phase-a-design.md`.

## Decision

Absorb the next useful Gateway layer without opening a new HTTP or WebSocket
surface yet.

Phase B adds:

- a `gateway.hello` RPC that returns a versioned handshake snapshot:
  protocol version, requested role, negotiated scopes, feature names, method
  catalog, and policy notes;
- dynamic scope resolution in the RPC descriptor catalog;
- a read-only `gateway.resolve_scope` diagnostic method for clients and tests;
- the first parameter-sensitive rule on `workspace.patch.propose`: ordinary
  relative patch paths resolve to `write`, while absolute, empty, or `..`
  paths resolve to `admin`.

This is a skeleton, not an auth grant. The daemon still uses its existing local
Unix socket and remote TCP origin rules. Future WS/HTTP Gateway transports must
consume this catalog and resolver rather than creating independent policy.

## Tradeoff

Rejected options:

- Real WebSocket Gateway first: closer to OpenClaw, but it would force auth,
  connection lifecycle, replay, and protocol compatibility decisions before the
  policy substrate is ready.
- OpenAI-compatible `/v1` first: product-visible, but unsafe without a role and
  scope contract underneath it.

Chosen option:

- Land the policy mechanics first. It has low blast radius, improves the
  existing daemon immediately, and gives future Gateway transports a shared
  source of truth.

## Components

- `go/rpc`: owns `Role`, `HelloRequest`, `HelloResponse`, `ScopeResolver`, and
  descriptor fields such as `dynamic_scope`.
- `go/daemon`: registers Gateway methods and daemon-specific dynamic resolvers.
- `docs/rpc-api.md` and `protocol/jsonrpc/methods.json`: document the new
  methods and result shapes.

## Non-goals

- No WebSocket listener in this phase.
- No HTTP `/v1/*` compatibility endpoint in this phase.
- No direct `/tools/invoke` in this phase.
- No bearer-token or Nebutra identity changes in this phase.

## Testing

- Unit-test scope negotiation and dynamic resolver behavior in `go/rpc`.
- Exercise `gateway.hello` and `gateway.resolve_scope` through daemon handler
  coverage.
- Run targeted Go tests, full Go tests, and core race tests.
