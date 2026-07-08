# RPC API

Transport (MVP): **JSON-RPC 2.0 over unix socket** (`~/.carina/daemon.sock`) or stdio. Optional remote TCP, WebSocket Gateway, and HTTP Gateway listeners are disabled by default. gRPC is a later optimization. Machine-readable registry: [`protocol/jsonrpc/methods.json`](../protocol/jsonrpc/methods.json).

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
| `context.status` | local-only native context engine and bundled Headroom status |
| `context.doctor` | local-only context engine health probe |
| `context.stats` | local-only local/Headroom compression counters |
| `context.compress` | local-only diagnostic compression call |
| `context.retrieve` | local-only diagnostic retrieval by Headroom CCR hash/ref |

Carina's daemon now registers RPC methods through a descriptor catalog. The
descriptor is the authority for remote exposure and future Gateway
role/scope negotiation; unclassified daemon handlers are refused in strict
mode. Operators can inspect the live contract with `carina gateway hello` and
the catalog with `carina gateway methods`.

`gateway.hello` is not an auth grant. It is a transport-neutral contract
snapshot for current JSON-RPC and future WebSocket/HTTP Gateway surfaces.
Actual authority remains enforced by transport origin, method descriptors, and
the capability kernel.

## Native Context Engine

Carina owns the context-engine boundary. Headroom is integrated as a managed,
private MCP transport behind that boundary when a bundled or explicitly
configured Headroom binary is available. The managed Headroom server is not
listed in the agent's public MCP tool list and cannot be called through the
agent `mcp` action surface.

The context RPCs are local-only:

| Method | Scope | Purpose |
|--------|-------|---------|
| `context.status` | `read` | report configured/effective engine, Headroom source, state directory, and managed MCP state |
| `context.doctor` | `read` | health probe used by `daemon.doctor` |
| `context.stats` | `read` | local counters plus Headroom stats when connected |
| `context.compress` | `write` | diagnostic compression call; not the agent transcript path |
| `context.retrieve` | `read` | diagnostic CCR retrieval by hash/ref; not remote-exposed |

Default `context_engine=auto` only enables bundled or explicitly configured
Headroom. A `headroom` executable found only on `PATH` is reported in status but
does not become the built-in engine, so release smoke tests cannot accidentally
pass because of a developer's global install.

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
- HTTP Gateway tokens should include explicit `routes` grants such as
  `/v1/models`, `/v1/*`, `/tools/invoke`, or `/plugins/*`; scope alone is not
  enough for HTTP dispatch.

Optional HTTP Gateway:

- enable explicitly with `carina-daemon -gateway-http 127.0.0.1:8787`, config
  key `gateway_http`, or env `CARINA_GATEWAY_HTTP`;
- HTTP Gateway refuses to start unless `gateway_token_signing_key_file` is
  configured;
- browser requests with an `Origin` header are rejected unless the exact value
  is configured through `-gateway-http-origins`, `gateway_http_origins`, or
  `CARINA_GATEWAY_HTTP_ORIGINS`;
- every request requires `Authorization: Bearer <gw1 token>` with
  `transport: "http"`, a matching route grant, and the required scope.

Implemented HTTP routes:

| Route | Purpose | Required scope | Required route grant |
|-------|---------|----------------|----------------------|
| `GET /v1/models` | list token-visible agent targets such as `carina`, `carina/default`, and `carina/<agent_id>` | `read` | `/v1/models` or `/v1/*` |
| `POST /v1/chat/completions` | translate OpenAI-style chat requests into normal Carina agent tasks | `write` | `/v1/chat/completions` or `/v1/*` |
| `POST /v1/responses` | translate OpenAI-style response requests into normal Carina agent tasks with bounded in-memory `previous_response_id` continuity | `write` | `/v1/responses` or `/v1/*` |
| `POST /tools/invoke` | invoke a read-only allowlist through the existing daemon/kernel paths | `read` | `/tools/invoke` |
| `/plugins/*` | authenticated fail-closed plugin HTTP reservation; no plugin route is installed by default | `read` | `/plugins/*` |

The `/v1` facade is agent-first, not provider-first. `model` selects a Carina
agent target (`carina`, `carina/default`, or `carina/<agent>`), not a backend
provider model. The route does not expose private provider configuration as
`/v1/models` data.

`/tools/invoke` requires an explicit `/tools/invoke` route grant. Its request
shape is `tool`, optional `action`, `args`, optional `agent_id`, optional
`session_key`, and optional `idempotency_key`. The current runtime only allows a
read-only method allowlist: daemon status/metrics/doctor, agent/command/session
listing, session get, and workspace tree/search/file reads. Process execution,
shell access, filesystem writes/deletes/moves, patch application, session
injection, node relay, Gateway mutation, plugin installation, and secret reads
are denied.

Plugin HTTP routes are extension surfaces, not ambient authority. The current
runtime reserves `/plugins/*` as an authenticated fail-closed route. Future
plugin handlers must run after core Gateway routes and cannot shadow `/v1`,
`/tools/invoke`, `/gateway`, JSON-RPC, session control, approval, secret, or
plugin-management routes. Gateway dispatch from plugin code is allowed only
inside authenticated request-local scope, only for methods declared by the
plugin route contract, and only within the inherited caller scope. Missing
request-local context fails closed.

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

## Memory API

| Method | Purpose |
|--------|---------|
| `memory.list` | list local memory entries for `target=memory` or `target=user` |
| `memory.context` | render the fenced recalled-memory context block for the session |
| `memory.status` | inspect local storage paths, identity scope, semantic-provider status, and Nebutra sync status |
| `memory.write` | add, replace, remove, or batch memory entries through the `MemoryWrite` capability |

`target=user` is scoped by Nebutra identity metadata when available. The daemon
uses `CARINA_NEBUTRA_IDENTITY_JSON` first, then the claims payload in
`CARINA_NEBUTRA_TOKEN`, then `CARINA_NEBUTRA_USER_ID`, and finally the local
fallback profile. Token claims are used only to choose a local memory scope;
they do not grant Gateway, kernel, or filesystem authority.

`memory.write` is local-only and control-plane-write. The daemon builds a
resource string from target, scope, action, operation count, and content hash,
then requests the `MemoryWrite` capability from the kernel. The built-in policy
defaults `MemoryWrite` to `requires_approval`. If a session policy or approval
mode returns `allowed`, the mutation is applied immediately and the response is
`{ "decision": PermissionDecision, "result": MemoryWriteResult }`; otherwise
the write is queued and the response contains only the decision.
`task.action.approve` applies the pending write, while `task.action.deny`
discards it. Audit payloads record target/scope/action, operation count, and
content hash, not raw memory text.

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

Kernel capability types also include mediated runtime capabilities that are not
exposed as standalone `capability.*` RPC methods, including `MemoryWrite`.

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
