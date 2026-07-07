#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

missing=()
for tool in go cargo zig; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    missing+=("$tool")
  fi
done
if (( ${#missing[@]} > 0 )); then
  printf 'release-check: missing required tool(s): %s\n' "${missing[*]}" >&2
  printf 'Install Go 1.25+, Rust 1.85+, and Zig 0.15.x, then retry.\n' >&2
  exit 127
fi

echo "==> build Go apps, Rust workspace, and Zig tools"
make all

echo "==> build release kernel service for Go integration tests"
cargo build --release -p carina-kernel --bin carina-kernel-service

echo "==> Go tests"
CARINA_KERNEL_BIN="$ROOT/target/release/carina-kernel-service" go test ./go/... ./apps/...

echo "==> Rust tests"
cargo test

echo "==> targeted Go race tests"
CARINA_KERNEL_BIN="$ROOT/target/release/carina-kernel-service" go test -race ./go/daemon ./go/config ./apps/carina-daemon

echo "release-check: ok"
