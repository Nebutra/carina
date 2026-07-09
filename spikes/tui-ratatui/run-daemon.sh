#!/usr/bin/env bash
# SPIKE harness: spawn an isolated real carina daemon + Rust kernel (same
# pattern as scripts/ci-gates.sh). State/socket/workspace under .rt/.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
RT="$ROOT/spikes/tui-ratatui/.rt"
[ -f "$RT/daemon.pid" ] && kill "$(cat "$RT/daemon.pid")" 2>/dev/null && sleep 0.5
rm -rf "$RT"
mkdir -p "$RT/state" "$RT/ws" "$RT/nopolicy"
printf 'hello from carina\nsecond line kept intact\n' > "$RT/ws/hello.txt"

KERNEL="${CARINA_KERNEL_BIN:-$ROOT/target/debug/carina-kernel-service}"
TOOLS="${CARINA_TOOLS_DIR:-$ROOT/zig/zig-out/bin}"
[ -x "$ROOT/bin/carina-daemon" ] || { echo "bin/carina-daemon missing"; exit 1; }
[ -x "$KERNEL" ] || { echo "kernel service missing: $KERNEL"; exit 1; }

CARINA_TOOLS_DIR="$TOOLS" "$ROOT/bin/carina-daemon" \
  -socket "$RT/d.sock" -state "$RT/state" \
  -kernel "$KERNEL" -tools "$TOOLS" -policy "$RT/nopolicy" \
  > "$RT/daemon.log" 2>&1 &
echo $! > "$RT/daemon.pid"
for _ in $(seq 1 300); do [ -S "$RT/d.sock" ] && break; sleep 0.05; done  # debug kernel can take ~10s
[ -S "$RT/d.sock" ] || { echo "daemon did not come up"; cat "$RT/daemon.log"; exit 1; }
echo "daemon up: pid $(cat "$RT/daemon.pid") sock $RT/d.sock"
