#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

missing=()
for tool in go cargo node npm python3 curl tar; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    missing+=("$tool")
  fi
done
if (( ${#missing[@]} > 0 )); then
  printf 'release-check: missing required tool(s): %s\n' "${missing[*]}" >&2
  printf 'Install Go 1.25+, Rust 1.85+, Node 24+, Python 3, curl, and tar; Zig 0.15.1 is installed from a pinned archive when needed.\n' >&2
  exit 127
fi

"$ROOT/scripts/zig-tool.sh" version >/dev/null

echo "==> build Go apps, Rust workspace, and Zig tools"
make all

echo "==> build release kernel service for Go integration tests"
cargo build --release -p carina-kernel --bin carina-kernel-service

echo "==> Go tests"
go test ./go/... ./apps/...
go test ./apps/carina-cli -run 'TestOperatorCommandsRequireExplicitCoordinates'

echo "==> npm launcher version matrix"
node packaging/npm/test.mjs
./scripts/test-version-matrix.sh

echo "==> Rust tests"
cargo test

echo "==> targeted Go race tests"
go test -race ./go/daemon ./go/config ./apps/carina-daemon

echo "==> Homebrew formula template"
./scripts/test-homebrew-formula.sh

echo "==> macOS signing/notarization automation"
./scripts/test-sign-and-notarize-release.sh

echo "==> packaged archive SDK conformance"
version="$(go run ./scripts/product-version.go)"
VERSION="$version" SKIP_BUILD=1 SKIP_HEADROOM=1 ./scripts/package-release.sh
ARCHIVE="$ROOT/dist/carina_${version}_$(go env GOOS)_$(go env GOARCH).tar.gz" ./scripts/test-packaged-conformance.sh

echo "release-check: ok"
