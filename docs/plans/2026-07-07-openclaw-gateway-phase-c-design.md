# OpenClaw Gateway Absorption Phase C: Default-Off WebSocket Skeleton

Source review: `docs/research/openclaw-gateway-source-review.md`.
Builds on Phase A: `docs/plans/2026-07-07-openclaw-gateway-phase-a-design.md`.
Builds on Phase B: `docs/plans/2026-07-07-openclaw-gateway-phase-b-design.md`.

## Decision

Add a WebSocket Gateway skeleton that is disabled by default and delegates all
authority and discovery to the existing daemon Gateway contract.

Phase C should introduce only the transport shell:

- a default-off WebSocket listener or upgrade route guarded by explicit local
  configuration;
- an exact browser `Origin` allowlist so web pages cannot silently connect to a
  loopback Gateway;
- a connection bootstrap that calls the existing `gateway.hello` contract
  instead of inventing a second handshake shape;
- per-message method dispatch that reads the existing method descriptors;
- parameter-sensitive authorization checks through the existing dynamic scope
  resolver;
- the same local/remote origin policy already used by the daemon RPC server.

The point is to make the future Gateway endpoint structurally real without
making it product-visible or more powerful than JSON-RPC. The skeleton should
fail closed when disabled, when origin policy rejects the connection, when a
method is not described, or when the resolved scope exceeds the negotiated
role/scope envelope.

## Transport Shape

The WebSocket surface should be a thin adapter over the daemon RPC server, not
a parallel Gateway implementation.

Suggested flow:

1. Startup leaves WebSocket Gateway disabled unless an explicit local flag or
   config value opts in.
2. An enabled upgrade request is treated as remote transport even when bound to
   loopback, so it stays constrained by descriptor `remote` flags and the
   remote kill-switch.
3. The first client message is a hello request using the `gateway.hello`
   request fields: `protocol_version`, `client_id`, `role`, `scopes`,
   `capabilities`, and `user_agent`.
4. The server returns the existing hello response shape: protocol version,
   requested role, negotiated role/scopes, features, method catalog, auth
   notes, and policy notes.
5. Later request messages carry JSON-RPC-compatible `id`, `method`, and
   `params` fields so dispatch can reuse the current daemon handlers.
6. Stream methods can be represented as long-lived subscriptions only when the
   descriptor marks `stream: true`; non-stream methods stay request/response.

No new method catalog, scope enum, role enum, or policy record should be added
for WebSocket. If the existing catalog cannot express a WebSocket decision, the
catalog should be extended first and then consumed by all transports.

## Policy Reuse

Phase C must preserve the Phase A/B policy stack:

- `gateway.hello` remains discovery and negotiation, not an auth grant.
- `MethodDescriptor` remains the source of truth for method name, baseline
  scope, `remote`, `stream`, `advertise`, `dynamic_scope`, and
  `control_plane_write`.
- Dynamic scope resolution remains parameter-sensitive and transport-neutral.
  For example, `workspace.patch.propose` must continue resolving safe relative
  paths to `write` and path escapes to `admin`.
- Origin policy remains transport-level authority. A remote WebSocket
  connection must not gain access to methods unavailable to the current remote
  TCP transport.
- Browser `Origin` is a separate HTTP header boundary: non-browser/native
  clients may omit it, but browser clients must match the configured allowlist.
- The capability kernel remains the final side-effect authority after Gateway
  transport and descriptor checks pass.

This means the WebSocket skeleton has two checks before dispatch:

1. Is this method exposed for this connection origin and not blocked by the
   remote kill-switch?
2. Does the resolved effective scope fit inside the role/scopes negotiated by
   `gateway.hello`?

Both checks should deny by default on missing descriptors, resolver errors,
unsupported protocol versions, unknown roles, unknown scopes, malformed params,
or disabled transport state.

## Lifecycle

The skeleton should keep connection lifecycle intentionally small:

- accept only after the default-off gate and origin policy pass;
- require hello before any method call;
- track the negotiated role/scopes on the connection;
- cap message size and process requests through the existing blocking JSON-RPC
  loop;
- close cleanly on malformed frames, protocol violations, or policy denials
  that indicate client confusion;
- emit ordinary JSON-RPC errors for method-level denials that do not require
  closing the socket.

No replay, reconnect, pairing, cross-device identity, plugin route tunneling,
or OpenAI compatibility state belongs in this phase.

## Non-goals

- No `/v1` endpoint in this phase.
- No `/tools/invoke` endpoint in this phase.
- No new auth grant in this phase.
- No Nebutra pairing yet.
- No new method registry separate from the existing daemon descriptors.
- No product-visible remote access expansion beyond current origin policy.

## Testing

Phase C implementation should be verified with focused transport tests:

- disabled-by-default startup refuses or omits the WebSocket surface;
- enabled WebSocket hello returns the same contract as `gateway.hello`;
- unsupported protocol versions, roles, and scopes fail closed;
- descriptor `remote` and the remote kill-switch apply to WebSocket requests;
- dynamic scope resolution gates mixed-risk methods before dispatch;
- stream requests require `stream: true` descriptors;
- existing JSON-RPC daemon behavior remains unchanged.

The acceptance bar is a transport skeleton that proves reuse of the existing
Gateway policy substrate. It should not make `/v1`, direct tool invoke, new
auth grants, or Nebutra pairing easier to accidentally expose.
