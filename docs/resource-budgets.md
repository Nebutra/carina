# Runtime Resource Budgets

Carina treats multi-session resource use as an observable operating constraint,
not a marketing benchmark. `daemon.doctor` and `daemon.metrics` expose the same
low-frequency `resources` snapshot so operators can compare session growth over
time and investigate compaction or cache pressure.

## Measurement definitions

| Field | Meaning | Non-claim |
|---|---|---|
| `process.rss_bytes` | Current resident bytes read from Linux `/proc/self/statm` | Not PSS; not attributable to one session |
| `process.go_heap_alloc_bytes` | Live heap bytes reported by Go `runtime.MemStats` | Not total process memory |
| `process.go_heap_sys_bytes` | Virtual memory obtained for the Go heap | Not resident memory |
| `sessions.items[].checkpoint_bytes` | JSON-serialized bytes in the latest durable checkpoints for that session's runs | Not heap or RSS |
| `sessions.items[].compactions` | Durable compaction receipts in those checkpoints | Not the number of provider requests |
| `caches.artifact_store` | Authoritative artifact-store operation and GC counters | Not a byte-accurate view of OS page cache |

On platforms where current process RSS is not implemented, or when procfs is
unavailable, `rss_available` is false and no numeric RSS estimate is emitted.
Carina does not substitute peak RSS, Go heap size, or `process RSS / sessions`
for a missing measurement. It does not report PSS without a real platform
collector.

## Initial operating principles

- Sample doctor/metrics on demand or at a low external cadence. The daemon does
  not add a background polling loop for this surface.
- Track process RSS and Go heap as separate curves. A rising heap with flat
  session/checkpoint counters suggests a different investigation than growing
  durable checkpoints across many sessions.
- Compare per-session checkpoint bytes and compaction receipts to find context
  growth. These counters are attributable even when memory pages are shared.
- Treat artifact quota rejects and repeated GC errors as capacity signals; do
  not infer them from RSS.
- Establish deployment warning thresholds from measured baselines on the
  actual platform and workload. A repository-wide MB limit would be misleading
  across model adapters, index sizes, and worker topology.

## Budget enforcement boundary

This observability surface does not kill sessions or change policy decisions.
Operators may alert on sustained process RSS, heap, checkpoint growth, artifact
quota rejects, or compaction-circuit failures. A future hard cap must define its
scope, sampling source, grace interval, and governed recovery action before it
can terminate or pause work.

Resource snapshots contain session identifiers and counters only. They never
include prompts, transcript text, workspace paths, credentials, or reasoning.
