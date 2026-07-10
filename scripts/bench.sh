#!/usr/bin/env bash
# Carina performance benchmarks (PRD section 13). The PRD defines a 10k-file
# scan and a separate "medium repo" grep target. This reproducible contract
# keeps scan at 10k files and defines the medium grep corpus as a 5k-file
# subtree with a selective query at a fixed 1% hit density.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
source "$ROOT/scripts/bench-lib.sh"

TOOLS="${CARINA_TOOLS_DIR:-$ROOT/zig/zig-out/bin}"
CARINA="${CARINA_BIN:-$ROOT/bin/carina}"
N="${1:-10000}"   # file count
GREP_MEDIUM_FILES=5000
GREP_MATCH_EVERY=100
TOOL_SAMPLES="$(bench_threshold CARINA_BENCH_TOOL_SAMPLES 5)" || exit $?

SCAN_LIMIT_MS="$(bench_threshold CARINA_BENCH_SCAN_THRESHOLD_MS 1000)" || exit $?
GREP_LIMIT_MS="$(bench_threshold CARINA_BENCH_GREP_THRESHOLD_MS 300)" || exit $?
PATCH_LIMIT_MS="$(bench_threshold CARINA_BENCH_PATCH_THRESHOLD_MS 50)" || exit $?
STARTUP_LIMIT_MS="$(bench_threshold CARINA_BENCH_STARTUP_THRESHOLD_MS 100)" || exit $?
STARTUP_WARMUPS="$(bench_threshold CARINA_BENCH_STARTUP_WARMUPS 5)" || exit $?
STARTUP_SAMPLES="$(bench_threshold CARINA_BENCH_STARTUP_SAMPLES 9)" || exit $?

bench_positive_int file_count "$N" || exit $?
if [ "$N" -lt "$GREP_MEDIUM_FILES" ]; then
  GREP_MEDIUM_FILES="$N"
fi
for tool in carina-scan carina-grep carina-patch-native; do
  [ -x "$TOOLS/$tool" ] || { echo "missing benchmark tool: $TOOLS/$tool" >&2; exit 2; }
done
[ -x "$CARINA" ] || { echo "missing CLI binary: $CARINA" >&2; exit 2; }

echo "effective thresholds:"
printf "  scan:         <%d ms  (CARINA_BENCH_SCAN_THRESHOLD_MS)\n" "$SCAN_LIMIT_MS"
printf "  grep:         <%d ms  (CARINA_BENCH_GREP_THRESHOLD_MS)\n" "$GREP_LIMIT_MS"
printf "  patch apply:  <%d ms  (CARINA_BENCH_PATCH_THRESHOLD_MS)\n" "$PATCH_LIMIT_MS"
printf "  warm startup: <%d ms  (CARINA_BENCH_STARTUP_THRESHOLD_MS; median of %d samples after %d warmups)\n" \
  "$STARTUP_LIMIT_MS" "$STARTUP_SAMPLES" "$STARTUP_WARMUPS"
printf "measurement: native tool gates use the median of %d samples with no explicit prewarm;\n" "$TOOL_SAMPLES"
echo "             first-sample latency is reported separately and is not labelled cold."

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
echo "generating $N files..."
workload_summary=$(python3 "$ROOT/scripts/generate-bench-repo.py" \
  "$TMP" "$N" "$GREP_MEDIUM_FILES" "$GREP_MATCH_EVERY") || exit $?
read -r generated_files grep_files grep_expected_hits grep_needle <<< "$workload_summary" || exit $?
printf "workload: scan=%d files; grep medium=%d files; needle=%s; %d expected hits (1 per %d files)\n" \
  "$generated_files" "$grep_files" "$grep_needle" "$grep_expected_hits" "$GREP_MATCH_EVERY"

# scan
scan_summary=$(python3 "$ROOT/scripts/measure-command-ms.py" \
  --samples "$TOOL_SAMPLES" --summary -- "$TOOLS/carina-scan" "$TMP") || exit $?
read -r scan_first_ms scan_ms <<< "$scan_summary" || exit $?

# grep
grep_summary=$(python3 "$ROOT/scripts/measure-command-ms.py" \
  --samples "$TOOL_SAMPLES" --summary -- \
  "$TOOLS/carina-grep" "$grep_needle" "$TMP/medium") || exit $?
read -r grep_first_ms grep_ms <<< "$grep_summary" || exit $?

# Validate the measured workload after sampling, so validation does not act as
# an implicit prewarm. Parse the tools' structured summaries rather than
# inferring correctness from process exit status.
scan_actual_files=$("$TOOLS/carina-scan" "$TMP" | python3 -c '
import json, sys
summary = None
for line in sys.stdin:
    value = json.loads(line)
    if "summary" in value:
        summary = value["summary"]
if summary is None:
    raise SystemExit("scan summary missing")
print(summary["files"])
') || exit $?
grep_actual_hits=$("$TOOLS/carina-grep" "$grep_needle" "$TMP/medium" | python3 -c '
import json, sys
summary = None
for line in sys.stdin:
    value = json.loads(line)
    if "summary" in value:
        summary = value["summary"]
if summary is None:
    raise SystemExit("grep summary missing")
print(summary["matches"])
') || exit $?
[ "$scan_actual_files" -eq "$generated_files" ] || {
  echo "scan result mismatch: expected $generated_files files, got $scan_actual_files" >&2
  exit 1
}
[ "$grep_actual_hits" -eq "$grep_expected_hits" ] || {
  echo "grep result mismatch: expected $grep_expected_hits hits, got $grep_actual_hits" >&2
  exit 1
}

# patch apply (single file, atomic). Create its fixtures only after validating
# the scan/grep repo so their file-count contract remains exact.
echo "orig" > "$TMP/one.txt"; cp "$TMP/one.txt" "$TMP/one.pre"
plan=$(python3 -c 'import json,sys;print(json.dumps({"files":[{"path":sys.argv[1]+"/one.txt","new_content":"changed\n","snapshot":sys.argv[1]+"/one.pre","existed":True}]}))' "$TMP")
printf '%s\n' "$plan" > "$TMP/patch-plan.json"
patch_summary=$(python3 "$ROOT/scripts/measure-command-ms.py" \
  --samples "$TOOL_SAMPLES" --summary --stdin-file "$TMP/patch-plan.json" -- \
  "$TOOLS/carina-patch-native" apply) || exit $?
read -r patch_first_ms patch_ms <<< "$patch_summary" || exit $?

# CLI startup latency is deliberately distinct from model TTFT. Warmups and a
# median keep this native fast-path check stable on shared CI runners.
startup_ms=$(python3 "$ROOT/scripts/measure-command-ms.py" \
  --warmups "$STARTUP_WARMUPS" --samples "$STARTUP_SAMPLES" -- \
  "$CARINA" --version) || exit $?

echo ""
echo "results ($N files):"
printf "  scan median:  %5d ms  (first sample: %d; limit: <%d; files: %d)\n" \
  "$scan_ms" "$scan_first_ms" "$SCAN_LIMIT_MS" "$scan_actual_files"
printf "  grep median:  %5d ms  (first sample: %d; limit: <%d; hits: %d)\n" \
  "$grep_ms" "$grep_first_ms" "$GREP_LIMIT_MS" "$grep_actual_hits"
printf "  patch median: %5d ms  (first sample: %d; limit: <%d)\n" "$patch_ms" "$patch_first_ms" "$PATCH_LIMIT_MS"
printf "  warm startup: %5d ms  (limit: <%d; not model TTFT)\n" "$startup_ms" "$STARTUP_LIMIT_MS"

status=0
bench_under_limit "$scan_ms" "$SCAN_LIMIT_MS"       || { echo "  scan OVER limit"; status=1; }
bench_under_limit "$grep_ms" "$GREP_LIMIT_MS"       || { echo "  grep OVER limit"; status=1; }
bench_under_limit "$patch_ms" "$PATCH_LIMIT_MS"     || { echo "  patch OVER limit"; status=1; }
bench_under_limit "$startup_ms" "$STARTUP_LIMIT_MS" || { echo "  warm startup OVER limit"; status=1; }
[ $status -eq 0 ] && echo "BENCH OK" || echo "BENCH: some targets missed"
exit $status
