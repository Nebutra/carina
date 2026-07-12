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

## Worker Pool Affinity

`--pool <tag>` (repeatable) declares a `worker_pool:<tag>` capability at
registration time, e.g. `--pool gpu-heavy --pool eu-west`. A streaming
workflow step declaring `"remote": true, "affinity": {"worker_pool":
"gpu-heavy"}` (see [workflows.md](workflows.md#remote-dispatch)) can only be
leased by a worker that registered with that exact tag — the daemon's
`worker.register` handler is the authoritative validator (at most 8 tags per
worker, each 1-64 lowercase letters/digits/dash/underscore); `carina-worker`'s
own `--pool` flag parsing is a client-side fast-fail mirror, not the trust
boundary. A worker started without `--pool` can only ever lease steps that
declare no affinity requirement at all.

Registering through `carina worker register <name> [remote|ci] --pool <tag>`
(rather than running the `carina-worker` binary) sets the same tags via the
same RPC field.

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
  "patches": ["patch_01J..."],
  "usage": {
    "input_tokens": 1200,
    "output_tokens": 340,
    "cache_read_tokens": 80,
    "cache_write_tokens": 0
  }
}
```

`status` is one of `completed`, `failed`, or `degraded`. `patches` contains patch
identifiers already created through an authorized integration; a path or an
arbitrary diff is not a patch identifier. The worker rejects unknown fields,
unsupported schema versions, multiple JSON values, output larger than 4 MiB,
empty patch identifiers, and more than 1024 patch identifiers.

`usage` is optional for executors that do not invoke a metered model. When it
is present, every count must be non-negative and the combined result must not
exceed one billion tokens. The daemon records it inside the same fenced,
idempotent lease-report transaction as the terminal result, so retries cannot
double-count spend. Streaming workflow budgets include reported remote usage;
an older or uninstrumented executor that omits `usage` remains explicitly
`unmetered`, never a measured zero.

A non-zero exit, timeout, invalid JSON, or invalid result contract is reported as
`failed`. Executor stderr is kept out of the task summary because it may contain
host-local or sensitive details.

### Publishing to a swarm channel

If the leased task is a streaming-workflow step declared `"remote": true`
(see [workflows.md](workflows.md#remote-dispatch)), the executor may include
an optional `channel_messages` array to publish into that run's swarm
channel — the remote counterpart of the `swarm_publish` tool an in-process
step calls directly, since the executor has no in-process tool-dispatch loop
to call it through:

```json
{
  "schema_version": "carina.worker.result.v1",
  "status": "completed",
  "summary": "Training run finished",
  "patches": [],
  "channel_messages": [
    {"channel": "progress", "payload": {"status": "epoch 10/10 complete"}}
  ]
}
```

At most 64 entries, each with a non-empty `channel` (max 128 characters);
`payload` is arbitrary JSON. These are delivered as a **batch when
work.report is called**, not continuously while the executor runs — the
result contract is one JSON value at the end, not a stream, so a remote
step's channel activity is coarser than a local step's (which can publish
at any point mid-run). A task that isn't a swarm-workflow dispatch simply
has nowhere to route `channel_messages`; the daemon accepts and ignores
them rather than failing the report. Governed by the same
`Capability::SwarmMessage` gate and per-channel retention cap
(`docs/workflows.md`'s [live inter-step messaging](workflows.md#live-inter-step-messaging-swarm-channels)
section) as an in-process publish — an invalid or denied entry is skipped
and audited, not a fatal error for the whole report.

## Lease And Shutdown Semantics

The worker polls only when it has a free concurrency slot, renews each active lease,
and reports a terminal result only while it still owns that lease. Empty queues use
bounded exponential backoff. Two consecutive renewal failures, an explicit daemon
cancellation, or a lease-ownership error cancels the executor and suppresses a stale
terminal report.

Terminal reporting keeps lease renewal active and retries at most three times with
bounded exponential backoff, but only for clearly transient transport/502/503/504
failures. Authentication, validation, stale-generation, cancellation, and lost-lease
errors are never retried. Every retry uses the identical
`task_id`+`lease_generation`+result payload; once renewal says the lease is invalid,
the worker cancels the report path immediately so an old generation cannot overwrite
new work.

On SIGINT or SIGTERM, the worker stops polling and drains existing executors. Work
that finishes inside `--drain-timeout` is reported normally. At the drain deadline,
remaining executors are cancelled and their leases are left for the daemon's
at-least-once reaper; they are not falsely reported as completed.

On Darwin and Linux, every executor runs in its own process group and cancellation
kills the whole group. On Windows 10+, the worker creates a
`KILL_ON_JOB_CLOSE` Job and supplies it through the creation-time Job List
attribute, so the executor belongs to the Job before its first instruction can
run. Cancellation, timeout, or executor completion closes the Job and terminates
remaining descendants. No process-name scanning or post-start assignment race is
used.

Workers register a typed `process_tree_containment` value. Darwin/Linux workers
advertise `unix_pgrp_v1`; Windows workers advertise `windows_job_v1`; other
platforms advertise `none`. Dispatch
tasks may require `process_tree_containment` (any governed implementation) or an
exact implementation such as `process_tree_containment:unix_pgrp_v1`. The daemon
keeps an unmatched task queued and leases it only to a matching worker. Unknown
requirements fail closed at submission.

Dispatch tasks that omit `required_worker_capabilities` are leaseable by any
registered worker, including workers advertising containment `none`. Callers
that need fail-closed descendant cancellation must opt in by submitting with
`required_worker_capabilities: ["process_tree_containment"]`; such a task stays
queued until a worker with a governed containment implementation leases it.
Windows CI runs a descendant-process cancellation contract before the worker is
allowed to advertise `windows_job_v1`.

The daemon issues `worker_credential` once during registration. The worker sends it
with heartbeat, revoke, backpressure, poll, renew, and report calls. Treat that
credential as a process secret and do not place it in executor input, command-line
arguments, logs, or persisted configuration. The worker also removes
`CARINA_GATEWAY_TOKEN` from the executor environment before process start.
