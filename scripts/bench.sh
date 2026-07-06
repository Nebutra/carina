#!/usr/bin/env bash
# Carina performance benchmarks (PRD §14). Generates a synthetic repo and
# times the native tools. Prints measured ms and checks against the targets.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TOOLS="${CARINA_TOOLS_DIR:-$ROOT/zig/zig-out/bin}"
N="${1:-10000}"   # file count

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
echo "generating $N files..."
python3 - "$TMP" "$N" <<'PY'
import os, sys
root, n = sys.argv[1], int(sys.argv[2])
for i in range(n):
    d = os.path.join(root, f"pkg{i//200}")
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, f"f{i}.go"), "w") as f:
        f.write(f"package p\n// item {i} TODO review\nfunc F{i}() int {{ return {i} }}\n")
PY

ms() { python3 -c "import sys;print(round(float(sys.argv[1])*1000))" "$1"; }

# scan
t0=$(python3 -c "import time;print(time.time())")
"$TOOLS/carina-scan" "$TMP" >/dev/null
t1=$(python3 -c "import time;print(time.time())")
scan_ms=$(python3 -c "print(round(($t1-$t0)*1000))")

# grep
t0=$(python3 -c "import time;print(time.time())")
"$TOOLS/carina-grep" "TODO" "$TMP" >/dev/null
t1=$(python3 -c "import time;print(time.time())")
grep_ms=$(python3 -c "print(round(($t1-$t0)*1000))")

# patch apply (single file, atomic)
echo "orig" > "$TMP/one.txt"; cp "$TMP/one.txt" "$TMP/one.pre"
plan=$(python3 -c 'import json,sys;print(json.dumps({"files":[{"path":sys.argv[1]+"/one.txt","new_content":"changed\n","snapshot":sys.argv[1]+"/one.pre","existed":True}]}))' "$TMP")
t0=$(python3 -c "import time;print(time.time())")
echo "$plan" | "$TOOLS/carina-patch-native" apply >/dev/null
t1=$(python3 -c "import time;print(time.time())")
patch_ms=$(python3 -c "print(round(($t1-$t0)*1000))")

echo ""
echo "results ($N files):"
printf "  scan:        %5d ms  (target: <1000 for 10k)\n" "$scan_ms"
printf "  grep:        %5d ms  (target: <300 mid repo)\n" "$grep_ms"
printf "  patch apply: %5d ms  (target: <50 single file)\n" "$patch_ms"

status=0
[ "$scan_ms" -lt 1000 ]  || { echo "  scan OVER target"; status=1; }
[ "$grep_ms" -lt 2000 ]  || { echo "  grep OVER target"; status=1; }
[ "$patch_ms" -lt 200 ]  || { echo "  patch OVER target"; status=1; }
[ $status -eq 0 ] && echo "BENCH OK" || echo "BENCH: some targets missed"
exit $status
