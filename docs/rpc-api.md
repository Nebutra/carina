# RPC API

Transport (MVP): **JSON-RPC 2.0 over unix socket** (`~/.carina/daemon.sock`) or stdio. gRPC is a later optimization. Machine-readable registry: [`protocol/jsonrpc/methods.json`](../protocol/jsonrpc/methods.json).

Notifications (server → client) stream events; every payload conforms to [`protocol/schemas/`](../protocol/schemas/).

## Gateway / Daemon API

| Method | Purpose |
|--------|---------|
| `gateway.hello` | versioned Gateway handshake snapshot: requested role, negotiated scopes, feature list, method catalog, policy notes |
| `gateway.methods` | live method catalog: method name, scope, remote exposure, stream flag, discovery flag, control-plane-write metadata |
| `gateway.resolve_scope` | local-only diagnostic for resolving a method's effective scope from request params |
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
