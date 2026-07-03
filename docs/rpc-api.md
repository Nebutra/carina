# RPC API

Transport (MVP): **JSON-RPC 2.0 over unix socket** (`~/.pi-os/daemon.sock`) or stdio. gRPC is a later optimization. Machine-readable registry: [`protocol/jsonrpc/methods.json`](../protocol/jsonrpc/methods.json).

Notifications (server → client) stream events; every payload conforms to [`protocol/schemas/`](../protocol/schemas/).

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
| `workspace.tree` | file tree (via `pi-scan`) |
| `workspace.search` | structured search (via `pi-grep`) |
| `workspace.file.get` | read a file (FileRead capability) |
| `workspace.patch.propose` / `apply` / `rollback` | transactional patch operations |

## Capability API (kernel-facing)

`capability.file.read` · `capability.file.write` · `capability.command.exec` · `capability.network.access` · `capability.secret.read` · `capability.patch.apply`

Each returns a `PermissionDecision` (see schema). Side effects only proceed on `allowed`.

## Worker API

`worker.register` · `worker.heartbeat` · `worker.assign` · `worker.output.stream` · `worker.revoke`

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
