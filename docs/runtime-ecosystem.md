# Runtime Ecosystem Contracts

Carina keeps orchestration and extension state outside the model context. The
daemon exposes typed JSON-RPC contracts; CLI, IDE, web, and mobile clients are
renderers and operators of the same state machine.

## Workflow control plane

`workflow.run/list/detail/pause/resume/stop/restart/save` operate on durable run
records. Details include step status, progress, token totals, and cost. Pause
and stop are cooperative at DAG step boundaries: an already-running governed
subagent reaches its boundary, then no new step starts. Restart creates a new
run ID and preserves the attempt lineage. Save writes a normal Carina command,
not executable workflow state.

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

## SDK conformance

Go, TypeScript, and Python expose typed workflow, worker, approval, doctor,
agent inventory, channels, and extension calls. The protocol registry remains
authoritative. Packaged-daemon CI can opt into a real read-only smoke test:

```bash
CARINA_CONFORMANCE_SOCKET=/path/to/daemon.sock go test ./sdk/go -run RealDaemon
```
