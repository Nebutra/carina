#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
version="${VERSION:-$(go run ./scripts/product-version.go)}"
arch="${GOARCH:-$(go env GOARCH)}"
dist="${DIST:-$ROOT/dist}"
archive="${ARCHIVE:-$dist/carina_${version}_linux_${arch}.tar.gz}"

case "$arch" in
  amd64) nfpm_arch=amd64 ;;
  arm64) nfpm_arch=arm64 ;;
  *) echo "package-linux: unsupported architecture: $arch" >&2; exit 2 ;;
esac
command -v nfpm >/dev/null || { echo "package-linux: nfpm is required" >&2; exit 127; }
[[ -f "$archive" ]] || { echo "package-linux: archive not found: $archive" >&2; exit 1; }

work="$(mktemp -d "${TMPDIR:-/tmp}/carina-linux-package.XXXXXX")"
trap 'rm -rf "$work" "$ROOT/dist/stage"' EXIT
tar -xzf "$archive" -C "$work"
mapfile -t roots < <(find "$work" -mindepth 1 -maxdepth 1 -type d -print)
[[ ${#roots[@]} -eq 1 ]] || { echo "package-linux: archive must contain one top-level directory" >&2; exit 1; }
stage="${roots[0]}"
mkdir -p "$ROOT/dist"
rm -rf "$ROOT/dist/stage"
cp -R "$stage" "$ROOT/dist/stage"

for binary in carina carina-daemon carina-worker carina-tui carina-kernel-service carina-diff carina-grep carina-patch-native carina-pty carina-run carina-scan; do
  [[ -x "$ROOT/dist/stage/bin/$binary" ]] || { echo "package-linux: missing executable $binary" >&2; exit 1; }
done

deb="$dist/carina_${version}_linux_${arch}.deb"
rpm="$dist/carina_${version}_linux_${arch}.rpm"
VERSION="$version" NFPM_ARCH="$nfpm_arch" nfpm package --config packaging/linux/nfpm.yaml --packager deb --target "$deb"
VERSION="$version" NFPM_ARCH="$nfpm_arch" nfpm package --config packaging/linux/nfpm.yaml --packager rpm --target "$rpm"
for package in "$deb" "$rpm"; do
  digest="$(shasum -a 256 "$package" | awk '{print $1}')"
  printf '%s  %s\n' "$digest" "$(basename "$package")" > "$package.sha256"
done
echo "package-linux: $deb $rpm"
