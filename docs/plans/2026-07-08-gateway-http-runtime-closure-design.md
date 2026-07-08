# Gateway HTTP Runtime Closure

## Decision

Land a default-off HTTP Gateway runtime that closes the remaining OpenClaw
absorption TODOs without turning Carina into an ambient public API server.

This pass implements the smallest safe runtime surface:

- an explicit `gateway_http` listener;
- scoped Gateway token auth for every HTTP route;
- route grants in signed token claims;
- agent-first `/v1/models`, `/v1/chat/completions`, and `/v1/responses`;
- read-only `/tools/invoke` for approved diagnostic/control-plane methods;
- fail-closed plugin HTTP route handling;
- a minimal usable `carina-tui` status/session viewer.

Nebutra device/node pairing remains a product boundary, not runtime authority.
Runtime pairing is not implemented in this pass.

## Authority Model

HTTP Gateway starts only when configured explicitly. If no
`gateway_token_signing_key_file` is configured, startup fails closed because the
HTTP runtime has no scoped token verifier.

Each request must present:

- `Authorization: Bearer gw1...`;
- a token bound to `transport: "http"`;
- a scope that covers the endpoint;
- a route grant that covers the endpoint.

The signing key is daemon-only signing material. It is never accepted as a
bearer credential.

## Routes

`GET /v1/models` requires `read` and `/v1/models`. It returns Carina agent
targets such as `carina`, `carina/default`, and `carina/<agent>`.

`POST /v1/chat/completions` requires `write` and
`/v1/chat/completions`. The OpenAI-style `model` selects the Carina agent target,
not a backend provider model. The route submits a normal Carina task and returns
an OpenAI-shaped response containing the task/session ids and the current task
state.

`POST /v1/responses` requires `write` and `/v1/responses`. It accepts a simple
string input or message-like input, submits a normal Carina task, and tracks a
bounded in-memory `previous_response_id -> session_id` continuity map.

`POST /tools/invoke` requires `read` and `/tools/invoke`. It only dispatches a
small read-only allowlist: daemon status/metrics/doctor, agent/command/session
listing, session get, and workspace read/search/tree/file reads. Unknown or
mutating tool requests fail closed.

`/plugins/*` requires a matching route grant and fails closed with a clear
message. This reserves plugin HTTP routing while proving that plugin routes do
not get ambient daemon authority.

## Testing

Tests should cover:

- token route grants and canonical verification;
- HTTP auth failure without token, wrong route, wrong transport, or missing
  scope;
- `/v1/models` agent-first response shape;
- chat/responses task submission through the existing daemon path;
- `/tools/invoke` read-only allowlist and mutating denial;
- TUI output helpers.
