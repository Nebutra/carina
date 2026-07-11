# Remote Worker Executor Contract

`carina-worker` is an authenticated lease client. It does not clone repositories,
synchronize a host workspace, create a sandbox, or apply patches by itself. The
operator-supplied executor is responsible for provisioning a controlled workspace
or sandbox and for enforcing the environment's isolation policy.

## Start A Worker

```bash
carina-worker \
  --gateway wss://daemon.example/gateway \
  --gateway-token-file ~/.config/carina/worker-gateway.token \
  --name linux-ci-1 \
  --kind ci \
  --executor /opt/carina/bin/ci-executor \
  --executor-arg --sandbox \
  --max-concurrency 1
```

`--executor` names a program directly; it is not evaluated by a shell. Add each
argument with a separate `--executor-arg`. The default concurrency is one.

Remote workers use an authenticated `wss://` Gateway. Store the scoped worker
Gateway token in a regular file that is inaccessible to group and other users
(`chmod 600`); `--gateway-token-file` takes precedence over the
`CARINA_GATEWAY_TOKEN` environment variable. There is intentionally no command-line
token flag because command arguments are commonly exposed through process listings.
The first WebSocket frame is `gateway.hello` with role `worker` and requested scopes
`worker`, `read`, and `stream`. The token must grant those scopes and be bound to the
`ws` transport.

Plain `ws://` is accepted only for loopback gateways. `--server` is also restricted
to loopback addresses and exists for a local daemon or an operator-authenticated
tunnel, for example:

```bash
ssh -N -L 7777:127.0.0.1:7777 worker-gateway.example
carina-worker --server 127.0.0.1:7777 --executor /opt/carina/bin/ci-executor
```

The tunnel's authentication, encryption, host verification, and lifecycle remain
the operator's responsibility. For direct remote connectivity, use `wss://`.

Operational timing can be configured with:

- `--lease-ttl` (default `30s`)
- `--renew-interval` (default `10s`, required to be less than half the TTL)
- `--poll-min-backoff` and `--poll-max-backoff` (defaults `250ms` and `5s`)
- `--executor-timeout` (default `30m`)
- `--drain-timeout` (default `30s`)
- `--heartbeat` (default `10s`)

## Input

For each lease, the worker starts one executor process and writes the daemon's
leased task JSON to its stdin. The executor must treat the task as untrusted input.
The task includes identifiers and intent, but it does not imply that a workspace
has been materialized on the worker host.

Executor logs belong on stderr. Stdout is reserved for exactly one result object.

## Output

The executor must exit zero and emit one JSON value on stdout:

```json
{
  "schema_version": "carina.worker.result.v1",
  "status": "completed",
  "summary": "Tests passed and the requested change was produced",
  "patches": ["patch_01J..."]
}
```

`status` is one of `completed`, `failed`, or `degraded`. `patches` contains patch
identifiers already created through an authorized integration; a path or an
arbitrary diff is not a patch identifier. The worker rejects unknown fields,
unsupported schema versions, multiple JSON values, output larger than 4 MiB,
empty patch identifiers, and more than 1024 patch identifiers.

A non-zero exit, timeout, invalid JSON, or invalid result contract is reported as
`failed`. Executor stderr is kept out of the task summary because it may contain
host-local or sensitive details.

## Lease And Shutdown Semantics

The worker polls only when it has a free concurrency slot, renews each active lease,
and reports a terminal result only while it still owns that lease. Empty queues use
bounded exponential backoff. Two consecutive renewal failures, an explicit daemon
cancellation, or a lease-ownership error cancels the executor and suppresses a stale
terminal report.

On SIGINT or SIGTERM, the worker stops polling and drains existing executors. Work
that finishes inside `--drain-timeout` is reported normally. At the drain deadline,
remaining executors are cancelled and their leases are left for the daemon's
at-least-once reaper; they are not falsely reported as completed.

On Darwin and Linux, every executor runs in its own process group and cancellation
kills the whole group so descendant processes cannot survive a timed-out or revoked
lease. Other platforms fall back to terminating the direct executor process; their
executor implementation must not detach descendants.

This limitation is a deployment capability boundary, not a best-effort security
claim. Operators that require fail-closed descendant cancellation must schedule
such work only to Darwin/Linux workers until an equivalent native containment
primitive is implemented for the target platform. Carina does not currently claim
Windows descendant-process containment and does not emulate it by process-name
scanning.

Workers register a typed `process_tree_containment` value. Darwin/Linux workers
advertise `unix_pgrp_v1`; Windows and other platforms advertise `none`. Dispatch
tasks may require `process_tree_containment` (any governed implementation) or an
exact implementation such as `process_tree_containment:unix_pgrp_v1`. The daemon
keeps an unmatched task queued and leases it only to a matching worker. Unknown
requirements fail closed at submission.

Remote dispatch defaults to requiring `process_tree_containment`; callers do
not need to opt in. Consequently the official Windows worker registers but does
not lease executor tasks until `windows_job_v1` passes conformance. This avoids
silently weakening cancellation just because a task omitted a capability list.

Windows must not advertise `windows_job_v1` until a native Job Object guard can
create the executor suspended, assign it to a kill-on-close Job, resume it, and
pass descendant-process conformance on Windows CI. `CREATE_NEW_PROCESS_GROUP`,
`taskkill /T`, and assigning a running process to a Job are not accepted as
equivalent containment because they leave escape races.

The daemon issues `worker_credential` once during registration. The worker sends it
with heartbeat, revoke, backpressure, poll, renew, and report calls. Treat that
credential as a process secret and do not place it in executor input, command-line
arguments, logs, or persisted configuration. The worker also removes
`CARINA_GATEWAY_TOKEN` from the executor environment before process start.
