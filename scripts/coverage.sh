#!/usr/bin/env bash
# Carina test coverage report (PRD §15). Reports measured coverage honestly;
# does not fake thresholds.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export CARINA_KERNEL_BIN="${CARINA_KERNEL_BIN:-$ROOT/target/release/carina-kernel-service}"

echo "=== Go coverage (go test -cover) ==="
go test -cover ./go/... 2>/dev/null | grep -E "coverage|no test files" || true

echo ""
echo "=== Rust coverage (cargo llvm-cov if available) ==="
if cargo llvm-cov --version >/dev/null 2>&1; then
  cargo llvm-cov --workspace --summary-only 2>/dev/null | tail -20
else
  echo "cargo-llvm-cov not installed; reporting test counts instead."
  echo "install with: cargo install cargo-llvm-cov && rustup component add llvm-tools-preview"
  cargo test --workspace 2>&1 | grep -E "test result" || true
fi
