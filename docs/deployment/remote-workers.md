# Remote Worker Deployment

`carina-worker` is a lease client. The daemon remains the scheduler and policy
authority; the operator-supplied executor owns workspace and sandbox creation.
Use an authenticated `wss://` Gateway for remote hosts. Direct `--server` TCP is
restricted to loopback and is intended only for a local daemon or an
operator-authenticated tunnel.

## Production Profile

1. Install the release package and a separately reviewed executor.
2. Copy `packaging/systemd/worker.env.example` to `/etc/carina/worker.env` and
   set the Gateway URL, executor path, pool labels, concurrency, and timeouts.
3. Store the scoped worker token at `/etc/carina/worker.token`, owned by root
   with mode `0600`. The supplied unit exposes it through systemd credentials;
   it is never placed in the process environment or command history.
4. Install `packaging/systemd/carina-worker.service`, run
   `systemctl daemon-reload`, then enable and start the service.

The executor reads one `carina.worker.task.v1` JSON object from stdin and emits
one `carina.worker.result.v1` object on stdout. It should report measured token
usage when available. Missing usage is shown as unmetered and never counted as
zero. See [worker executor contract](../worker-executor.md).

## Readiness And Health

- `carina worker list` must show the expected name, kind, pool labels, and
  `online` state.
- The worker heartbeat must remain comfortably below its lease TTL.
- Run one canary workflow pinned to the worker pool and verify its output,
  audit trail, and token accounting before admitting normal work.
- Alert on repeated reconnects, lease expiry, executor timeouts, malformed
  result envelopes, and a sustained absence of successful canaries.

## Drain And Upgrade

Stop the service with `systemctl stop carina-worker`. SIGTERM stops new lease
polls and allows active work to drain up to `--drain-timeout`; after that the
worker cancels its executor process tree. Upgrade the package only after the
worker is offline in `carina worker list`, then restart and repeat the canary.
Roll pools one worker at a time so another compatible worker can retain
capacity.

On Unix, executor descendants share a process group. On Windows they are
created inside a kill-on-close Job Object. Cancellation and timeout therefore
terminate the executor tree rather than only its parent process.

## Container Profile

`packaging/docker/worker.Dockerfile` builds a non-root worker image. Supply the
executor in a derived image or mounted read-only volume, mount the token as a
secret file, and pass explicit worker flags. Do not bake credentials into an
image layer. The daemon image similarly runs as UID/GID `65532` and embeds the
pinned kernel service.

Registry publication, hosted Gateway DNS/TLS, tenant creation, and production
credentials are external provisioning work tracked in the Roadmap.
