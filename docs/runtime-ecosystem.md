# Runtime Ecosystem Contracts

Carina keeps orchestration and extension state outside the model context. The
daemon exposes typed JSON-RPC contracts; CLI, IDE, web, and mobile clients are
renderers and operators of the same state machine.

## Workspace runtime ownership

The default local topology is one detached, on-demand execution runtime per
canonical workspace. `go/localruntime` resolves the workspace, configuration
provenance, stable workspace/runtime IDs, and every runtime path into one
versioned spec. `go/localdaemon` then performs atomic connect-or-start under a
per-runtime advisory lock and accepts the endpoint only after
`runtime.describe` plus `runtime.initialize` prove the complete identity.

Runtime descriptors persist after shutdown and form a passive, read-only
registry. Owner records and sockets are ephemeral: graceful shutdown writes
`lifecycle=stopped` with `stopped_at`, then removes only the owner record and
socket that identify the exiting process. Stop refuses an unreachable or
mismatched endpoint, a reused PID running another executable, and active
background obligations unless `--force` is explicit.

Useful operator commands:

```bash
carina runtime start|status|stop|logs [--workspace PATH]
carina runtime mode workspace|legacy
carina runtimes [--json]
```

Fresh installs use workspace mode. When legacy global state is detected and no
decision exists, an interactive launch offers workspace mode, legacy mode, or
cancel; non-interactive commands still require an explicit environment or
persisted decision. The choice is reversible. Workspace and legacy state remain
separate and may coexist; neither mode imports or mutates the other.

## Workflow control plane

`workflow.run/list/detail/pause/resume/stop/restart/save` operate on durable run
records. Details include step status, progress, token totals, and cost. Pause
and stop are cooperative at DAG step boundaries: an already-running governed
subagent reaches its boundary, then no new step starts. Restart creates a new
run ID and preserves the attempt lineage. Save writes a normal Carina command,
not executable workflow state.

Workflows declare an `execution_mode`: `bsp` (the default; barrier-synchronized
waves, with pause/stop honored between waves) or `streaming` (each step starts
as soon as its dependencies resolve, with bounded concurrency, per-step
`fail_fast`, and failure isolated to a step's own dependents). Streaming runs
currently apply pause/stop via context cancellation rather than wave-boundary
checks.

## Trusted Channels and Monitor

An external sender must be registered locally with a minimum 32-byte HMAC
secret, allowed session IDs and event kinds, and a separate opt-in for
permission relay. `channel.event.inject` rejects unknown senders, invalid
signatures, stale timestamps, unauthorized targets, and ungranted permission
relays. Event IDs are deduplicated per sender. Accepted payloads become
structured `ExternalEvent` envelopes; payload text is never executed directly.

## Telemetry and cost attribution

Telemetry is disabled by default. `Options.TelemetryWriter` enables Carina's
versioned `carina-telemetry-json-v1` newline JSON format. It is not OTLP and
does not claim OpenTelemetry wire compatibility. Trace, metric, and log records can
attribute usage to tenant, workspace, session, workflow, step, task, provider,
model, plugin, and worker. Cost records carry request, input/output/cache token
counts, USD, and an estimated flag. A future OTLP exporter must perform an
explicit mapping instead of relabeling these records.

## Local marketplace

The marketplace only installs manifests below configured trusted local roots.
It validates runtime constraints, component kinds, dependency versions, source
digest, and estimated prompt-token cost. Extensions install disabled. Safe mode
atomically disables the inventory and refuses re-enable operations.

Supported components are `skill`, `hook`, `mcp`, `workflow`, `wasm`, `worker`,
and `artifact-adapter`. A manifest cannot request native execution. WASM still
runs through the capability kernel; MCP uses the governed manager.

## Product boundaries

Computer Use and Browser Use are worker kinds with explicit capabilities,
credentials, sandbox policy, lease cancellation, and audit events. They are not
ambient runtime powers. Artifacts are structured adapter outputs containing
type, schema/version, content reference, provenance, and policy labels. Rendering
belongs to Nebutra web/IDE/mobile surfaces and never grants filesystem or shell
authority back to the artifact.

Tool-output artifacts use bounded retention tiers: `ephemeral` defaults to 24
hours, `normal` to 30 days, and `pinned` to 180 days with a hard one-year
maximum. Pinning is therefore a longer operational retention choice, not an
unbounded legal hold. The store reports only low-cardinality aggregate counts
and byte totals; session, task, call, and artifact identifiers remain in audit
records rather than metric labels. Generic `artifact.stat` and `artifact.read`
RPC methods remain local-only. Any future remote download surface requires a
separate exact-scope, short-lived, single-use capability and download audit.

## SDK conformance

Go, TypeScript, and Python expose typed workflow, worker, approval, doctor,
agent inventory, channels, and extension calls. The protocol registry remains
authoritative. Packaged-daemon CI can opt into a real read-only smoke test:

```bash
CARINA_CONFORMANCE_SOCKET=/path/to/daemon.sock go test ./sdk/go -run RealDaemon
```

## Event compatibility and retry governance

Session attach and event streaming accept `compat` (default) or `canonical`.
Both modes use the same exclusive raw-audit cursor: filtered compatibility
events never change cursor positions, so a client may reconnect or switch modes
without duplicates or gaps. Stream subscription results expose the initial raw
cursor, replayed count, and effective mode. Every canonical event also carries
`raw_cursor`; clients persist the latest delivered cursor rather than counting
rendered events.

Provider retry budgets and circuit breakers are isolated by requested
`provider/model` route. An untargeted router request uses a separate stable
default-route key. Half-open permits exactly one concurrent probe; a successful
or non-retryable response closes the availability breaker, while a retryable
failure reopens it. Retry sleeps do not hold the probe permit or consume a
budget token until the next attempt is admitted.
