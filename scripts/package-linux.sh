#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$ROOT"
version="${VERSION:-$(go run ./scripts/product-version.go)}"; arch="${GOARCH:-$(go env GOARCH)}"; stage="$ROOT/dist/stage"
case "$arch" in amd64) nfpm_arch=amd64;; arm64) nfpm_arch=arm64;; *) echo "unsupported linux arch: $arch" >&2; exit 2;; esac
rm -rf "$stage"; mkdir -p "$stage/bin"
for app in carina carina-daemon carina-worker carina-tui; do
  source="./apps/$app"; [[ "$app" == carina ]] && source="./apps/carina-cli"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$stage/bin/$app" "$source"
done
CARGO_BUILD_TARGET="${CARGO_BUILD_TARGET:?set the Rust Linux target}" cargo build --release -p carina-kernel --bin carina-kernel-service --target "$CARGO_BUILD_TARGET"
cp "target/$CARGO_BUILD_TARGET/release/carina-kernel-service" "$stage/bin/"
tar -C "$stage" -czf "dist/carina_${version}_linux_${arch}.tar.gz" .
if command -v nfpm >/dev/null; then VERSION="$version" NFPM_ARCH="$nfpm_arch" nfpm package --config packaging/linux/nfpm.yaml --packager deb --target dist/; VERSION="$version" NFPM_ARCH="$nfpm_arch" nfpm package --config packaging/linux/nfpm.yaml --packager rpm --target dist/; else echo 'nfpm not installed; tar archive created, deb/rpm skipped' >&2; fi
"$ROOT/scripts/generate-sbom.sh" "$stage" "dist/carina_${version}_linux_${arch}.spdx.json"
