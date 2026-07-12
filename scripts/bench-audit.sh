#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
events="${CARINA_BENCH_AUDIT_EVENTS:-100}"
workers="${CARINA_BENCH_AUDIT_WORKERS:-4}"
min_eps="${CARINA_BENCH_AUDIT_MIN_EPS:-10}"
max_p99_ms="${CARINA_BENCH_AUDIT_MAX_P99_MS:-2000}"

for pair in "events:$events" "workers:$workers" "min_eps:$min_eps" "max_p99_ms:$max_p99_ms"; do
  name="${pair%%:*}"
  value="${pair#*:}"
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || { echo "bench-audit: $name must be a positive integer" >&2; exit 2; }
done

report="$(cd "$ROOT" && cargo run --quiet -p carina-audit --example audit_bench -- "$events" "$workers")"
python3 - "$report" "$min_eps" "$max_p99_ms" <<'PY'
import json, sys
report = json.loads(sys.argv[1])
min_eps = int(sys.argv[2])
max_p99 = int(sys.argv[3])
if report["events_per_second"] < min_eps:
    raise SystemExit(f'audit throughput {report["events_per_second"]:.2f}/s is below {min_eps}/s')
if report["p99_ms"] >= max_p99:
    raise SystemExit(f'audit p99 {report["p99_ms"]:.2f}ms is not below {max_p99}ms')
print(json.dumps(report, sort_keys=True))
PY
