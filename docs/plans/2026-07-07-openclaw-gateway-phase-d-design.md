# OpenClaw Gateway Absorption Phase D: Gated HTTP Surface Skeletons

Source review: `docs/research/openclaw-gateway-source-review.md`.
Builds on Phase A: `docs/plans/2026-07-07-openclaw-gateway-phase-a-design.md`.
Builds on Phase B: `docs/plans/2026-07-07-openclaw-gateway-phase-b-design.md`.
Builds on Phase C: `docs/plans/2026-07-07-openclaw-gateway-phase-c-design.md`.

## Decision

Implement the scoped Gateway token foundation, then document the next OpenClaw
Gateway slices as disabled-by-default future HTTP surfaces:

- agent-first OpenAI-compatible `/v1` routes;
- scoped direct `/tools/invoke`;
- plugin HTTP routes with request-local Gateway dispatch scope.

Phase D adds no HTTP listeners or plugin runtime behavior. It does add the
local-only token issuer/verifier needed by the already default-off WebSocket
Gateway and by future HTTP routes. Every future HTTP surface is gated by scoped
Gateway tokens; `gateway.hello` remains discovery and negotiation, not an auth
grant.

Scoped Gateway tokens are capability tokens whose authority is narrower than
local operator ownership. The implemented `gw1` token carries role, canonical
scopes, transport binding, issue time, expiry, subject, and policy notes.
Dispatch intersects token claims with method descriptors, dynamic scope
resolution, transport origin policy, and the capability kernel. Missing or
malformed token context fails closed once token verification is configured for a
transport.

Current implementation:

- `gateway_token_signing_key_file` / `CARINA_GATEWAY_TOKEN_SIGNING_KEY_FILE` /
  `-gateway-token-signing-key-file` explicitly enables local signing;
- the key file must be private and at least 32 bytes after trimming whitespace;
- `gateway.token.issue` is local-only, `admin`-scoped, control-plane-write, and
  registered only when the signing key file is configured;
- token scope requests must be explicit and are canonicalized against role
  maximums;
- WebSocket Gateway verifies `transport: "ws"` tokens before dispatch when a
  signer/verifier is configured;
- the signing key is never accepted directly as a bearer credential.

## Agent-First `/v1` HTTP

The `/v1` facade should be an agent API, not a provider-model API. Future
routes are reserved as:

- `GET /v1/models`;
- `POST /v1/chat/completions`;
- `POST /v1/responses`;
- `POST /v1/embeddings` after the agent path is proven.

`/v1/models` should list agent targets visible to the token, such as `carina`,
`carina/default`, and `carina/<agent_id>`. It should not expose private backend
provider catalogs by default. Chat Completions and Responses should translate
OpenAI-style requests into normal Carina agent runs using the agent selected by
`model`.

Backend provider overrides, if exposed at all, should be explicit headers or
params guarded by `admin`-level scoped Gateway tokens and validated against the
provider catalog visibility policy. Session continuity for `/v1/responses`
should be bounded, scoped, and auditable; reserved session namespaces must be
rejected.

## Scoped `/tools/invoke`

Direct tool invocation is useful only if it is not a bypass around Carina's
approval, audit, or capability model. The future route is reserved as:

- `POST /tools/invoke`.

The request skeleton is:

```json
{
  "tool": "string",
  "action": "string?",
  "args": {},
  "agent_id": "string?",
  "session_key": "string?",
  "idempotency_key": "string?"
}
```

The route should require a scoped Gateway token with an explicit tool-invoke
grant. Tool availability must be derived from the same policy chain used for
agent-visible tools and kernel capabilities. Process execution, shell access,
filesystem writes/deletes/moves, patch application, session injection, node
relay, Gateway mutation, plugin installation, and secret reads should remain
denied by default unless a future local owner policy grants them explicitly.

Blocked invocations should return structured denial results and audit events,
not partial side effects.

## Plugin HTTP Request Scope

Plugin HTTP routes are future extension points, not ambient authority. If
Carina adds them, plugin routes must run after core Gateway routes and must not
shadow `/v1`, `/tools/invoke`, `/gateway`, JSON-RPC, session control, approval,
secret, or plugin-management routes.

Protected plugin routes should require scoped Gateway tokens unless a manifest
declares a narrow public/static route. Route handlers may receive a derived
Gateway client only inside request-local scope created from the authenticated
request. The plugin manifest must declare which Gateway methods are callable
from that route, and each call must inherit the caller's token scope. Plugin
code outside that request-local context must have no ambient Gateway dispatch.

Missing auth context, missing route declaration, undeclared Gateway method
dispatch, expired token, or scope mismatch fails closed.

## Non-goals

- No HTTP implementation in this phase.
- No direct owner/admin bearer token.
- No token issuer unless explicitly configured with a local signing key file.
- No default-on OpenAI-compatible API.
- No provider-first model listing.
- No direct tool invocation bypassing the kernel.
- No ambient plugin access to daemon or Gateway methods.
- No Nebutra device/node pairing.

## Acceptance

Phase D is complete when scoped Gateway token issuing/verifying is implemented,
covered by tests, default-off without a signing key, and the HTTP/tool/plugin
surfaces remain documented as future disabled routes gated by those tokens.
