#!/usr/bin/env bash
# SPIKE harness: spawn a real carina daemon + rust kernel + zig tools in a temp dir.
# Prints "SOCKET=<path>" and "STATE=<dir>" on success; daemon PID in $STATE/daemon.pid.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
STATE="${1:-$(mktemp -d /tmp/carina-spike.XXXXXX)}"
mkdir -p "$STATE/ws"
echo "hello workspace" > "$STATE/ws/readme.txt"

KERNEL="${CARINA_KERNEL_BIN:-$ROOT/target/debug/carina-kernel-service}"
TOOLS="${CARINA_TOOLS_DIR:-$ROOT/zig/zig-out/bin}"
DAEMON="$ROOT/bin/carina-daemon"
[ -x "$DAEMON" ] || { echo "build first: go build -o bin/carina-daemon ./apps/carina-daemon" >&2; exit 1; }
[ -x "$KERNEL" ] || { echo "kernel missing: cargo build -p carina-kernel --bin carina-kernel-service" >&2; exit 1; }

CARINA_TOOLS_DIR="$TOOLS" "$DAEMON" \
  -socket "$STATE/d.sock" -state "$STATE/st" \
  -kernel "$KERNEL" -tools "$TOOLS" -offline \
  >"$STATE/daemon.log" 2>&1 &
echo $! > "$STATE/daemon.pid"
for _ in $(seq 1 100); do [ -S "$STATE/d.sock" ] && break; sleep 0.05; done
[ -S "$STATE/d.sock" ] || { echo "daemon did not come up; log:" >&2; cat "$STATE/daemon.log" >&2; exit 1; }
echo "SOCKET=$STATE/d.sock"
echo "STATE=$STATE"
