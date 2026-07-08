# RPC API

Transport (MVP): **JSON-RPC 2.0 over unix socket** (`~/.carina/daemon.sock`) or stdio. Optional remote TCP and WebSocket Gateway listeners are disabled by default. gRPC is a later optimization. Machine-readable registry: [`protocol/jsonrpc/methods.json`](../protocol/jsonrpc/methods.json).

Notifications (server → client) stream events; every payload conforms to [`protocol/schemas/`](../protocol/schemas/).

## Gateway / Daemon API

| Method | Purpose |
|--------|---------|
| `gateway.hello` | versioned Gateway handshake snapshot: requested role, negotiated scopes, feature list, method catalog, policy notes |
| `gateway.methods` | live method catalog: method name, scope, remote exposure, stream flag, discovery flag, control-plane-write metadata |
| `gateway.resolve_scope` | local-only diagnostic for resolving a method's effective scope from request params |
| `gateway.token.issue` | local-only scoped Gateway token issuer, registered only when `gateway_token_signing_key_file` is configured |
| `daemon.status` | daemon process/runtime status |
| `daemon.metrics` | runtime metrics |
| `daemon.doctor` | independent health probes |

Carina's daemon now registers RPC methods through a descriptor catalog. The
descriptor is the authority for remote exposure and future Gateway
role/scope negotiation; unclassified daemon handlers are refused in strict
mode. Operators can inspect the live contract with `carina gateway hello` and
the catalog with `carina gateway methods`.

`gateway.hello` is not an auth grant. It is a transport-neutral contract
snapshot for current JSON-RPC and future WebSocket/HTTP Gateway surfaces.
Actual authority remains enforced by transport origin, method descriptors, and
the capability kernel.

Optional WebSocket Gateway:

- enable explicitly with `carina-daemon -gateway-ws 127.0.0.1:8777`, config
  key `gateway_ws`, or env `CARINA_GATEWAY_WS`;
- endpoint path is `/gateway`;
- browser requests with an `Origin` header are rejected unless the exact value
  is configured through `-gateway-ws-origins`, `gateway_ws_origins`, or
  `CARINA_GATEWAY_WS_ORIGINS`;
- first text frame must be a JSON-RPC `gateway.hello` request;
- when `gateway_token_signing_key_file` is configured, `gateway.hello` must
  include a signed scoped Gateway token bound to `transport: "ws"`;
- later frames are JSON-RPC requests constrained by descriptor `remote`, the
  remote kill-switch, negotiated or token-bound scopes, and dynamic scope
  resolution.

Scoped Gateway token issuing:

- enable signing explicitly with `gateway_token_signing_key_file`, env
  `CARINA_GATEWAY_TOKEN_SIGNING_KEY_FILE`, or daemon flag
  `-gateway-token-signing-key-file`;
- the signing key file must be private (`0600`-style, not group/world
  readable) and at least 32 bytes after trimming whitespace;
- `gateway.token.issue` is local-only, requires `admin`, and is not registered
  when no signing key file is configured;
- requested token scopes must be explicit; empty scope requests do not default
  to role maximum;
- the issued token signs role, canonical scopes, transport binding, issue time,
  expiry, subject, and policy notes; the signing key itself is never accepted as
  a bearer credential;
- max TTL defaults to 900 seconds and can be configured with
  `gateway_token_max_ttl_seconds`, env
  `CARINA_GATEWAY_TOKEN_MAX_TTL_SECONDS`, or `-gateway-token-max-ttl`.

Future HTTP Gateway surfaces are reserved but not implemented or enabled by
default. They require scoped Gateway tokens; `gateway.hello` is still not an
auth grant. A future HTTP dispatch path must intersect token claims with route
grants, descriptor scopes, dynamic scope resolution, transport origin policy,
and the capability kernel before any side effect.

Reserved future HTTP routes:

| Route | Purpose | Default state |
|-------|---------|---------------|
| `GET /v1/models` | list token-visible agent targets such as `carina`, `carina/default`, and `carina/<agent_id>` | disabled |
| `POST /v1/chat/completions` | translate OpenAI-style chat requests into normal Carina agent runs | disabled |
| `POST /v1/responses` | translate OpenAI-style response requests into normal Carina agent runs with bounded scoped continuity | disabled |
| `POST /v1/embeddings` | optional later agent-first embeddings facade | disabled |
| `POST /tools/invoke` | direct tool invocation through the same policy, approval, audit, and kernel capability chain as agent-visible tools | disabled |

The `/v1` facade is agent-first, not provider-first. `model` selects a Carina
agent target; backend provider overrides require an explicit `admin`-scoped
Gateway token and provider catalog visibility checks. The route must not expose
private provider configuration as `/v1/models` data by default.

`/tools/invoke` requires an explicit tool-invoke token grant. Its request shape
is `tool`, optional `action`, `args`, optional `agent_id`, optional
`session_key`, and optional `idempotency_key`. Process execution, shell access,
filesystem writes/deletes/moves, patch application, session injection, node
relay, Gateway mutation, plugin installation, and secret reads remain denied by
default unless a future local owner policy enables them.

Future plugin HTTP routes are extension surfaces, not ambient authority. They
run after core Gateway routes and cannot shadow `/v1`, `/tools/invoke`,
`/gateway`, JSON-RPC, session control, approval, secret, or plugin-management
routes. Protected plugin routes require scoped Gateway tokens. Gateway dispatch
from plugin code is allowed only inside authenticated request-local scope, only
for methods declared by the plugin route contract, and only within the inherited
caller scope. Missing request-local context fails closed.

Scopes:

| Scope | Meaning |
|-------|---------|
| `read` | read-only status, list, replay, catalog, audit, and result methods |
| `write` | mutating session/task/workspace actions inside the local operator boundary |
| `admin` | high-risk control-plane, secret, config, policy, plugin, or approval actions |
| `worker` | remote worker lease protocol |
| `stream` | long-lived event subscriptions |

Dynamic scopes:

- `workspace.patch.propose` has a static baseline of `write`, but resolves to
  `admin` when params contain an empty path, absolute path, `.` path, or `..`
  path segment. The resolver is an early Gateway classification layer; the
  kernel remains the final side-effect authority.
- `session.add_dir` resolves to `write` only for an existing absolute directory
  contained by the session workspace; ambiguous, missing, symlink-escaping, or
  outside paths resolve to `admin`.
- `workspace.trust` resolves to `write` only when revoking trust for a clean
  absolute root; granting trust remains `admin`.
- `task.action.deny` resolves to `write` only for an ordinary deny against an
  existing session without an explicit approver; spoofed approver or ambiguous
  params resolve to `admin`.
- `task.action.approve` remains statically `admin`.

## Session API

| Method | Purpose |
|--------|---------|
| `session.create` | create a session bound to a workspace + permission profile |
| `session.get` | fetch session metadata |
| `session.list` | list sessions |
| `session.pause` / `session.resume` | suspend / continue |
| `session.close` | terminate |
| `session.export` | export as JSONL / SQLite bundle |
| `session.replay` | replay the event stream |
| `session.items` | replay the normalized item stream derived from audit events |

## Task API

| Method | Purpose |
|--------|---------|
| `task.submit` | submit a prompt/task into a session |
| `task.cancel` | cancel a running task |
| `task.status` | query task state |
| `task.events.stream` | subscribe to the task event stream |
| `task.action.approve` / `task.action.deny` | resolve pending approval requests |

## Workspace API

| Method | Purpose |
|--------|---------|
| `workspace.open` / `workspace.scan` | register + index a workspace |
| `workspace.tree` | file tree (via `carina-scan`) |
| `workspace.search` | structured search (via `carina-grep`) |
| `workspace.file.get` | read a file (FileRead capability) |
| `workspace.patch.propose` / `apply` / `rollback` | transactional patch operations |

## Capability API (kernel-facing)

`capability.file.read` · `capability.file.write` · `capability.command.exec` · `capability.network.access` · `capability.secret.read` · `capability.patch.apply`

Each returns a `PermissionDecision` (see schema). Side effects only proceed on `allowed`.

## Command / Audit / Secret / Plugin / Enterprise

- `command.exec` — propose a command; returns a decision (and result if allowed).
- `session.items` — normalized `thread.started` / `turn.started` / `item.*` / `turn.*` stream for UI and SDK consumers; `session.replay` remains the raw audit stream.
- `audit.report` — summary (violations, files, commands); `audit.export` — full bundle for centralized audit.
- `profile.describe` — capability-graph view of the session profile.
- `secret.grant` / `secret.request` — handle-based secrets; plaintext never crosses the boundary.
- `plugin.inspect` — declared permissions; `plugin.run` — run a WASM plugin (optional `signature_base64`).
- `task.action.approve` — accepts an optional `role` for role-based approval.
- `session.events.stream` — server-notification stream of session events.

## Worker API

`worker.register` · `worker.heartbeat` · `worker.list` · `worker.revoke`

## Example

```json
// request
{"jsonrpc":"2.0","id":1,"method":"session.create",
 "params":{"workspace_root":"/repo","profile":"safe-edit"}}

// response
{"jsonrpc":"2.0","id":1,
 "result":{"session_id":"sess_01J...","workspace_id":"ws_01J...","profile":"safe-edit"}}

// event notification
{"jsonrpc":"2.0","method":"event",
 "params":{"event_id":"evt_...","session_id":"sess_...","type":"CommandStarted",
           "timestamp":"2026-07-03T12:00:00Z","payload":{"command":"npm test"},
           "permission_decision_id":"perm_..."}}
```
