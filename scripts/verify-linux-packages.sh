#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="${DIST:-$ROOT/dist}"
VERSION="${VERSION:?VERSION is required}"

for arch in arm64 amd64; do
  for ext in deb rpm; do
    package="$DIST/carina_${VERSION}_linux_${arch}.$ext"
    checksum="$package.sha256"
    [[ -f "$package" && -f "$checksum" ]] || { echo "verify-linux-packages: missing $package or checksum" >&2; exit 1; }
    read -r expected filename < "$checksum"
    [[ "$filename" == "$(basename "$package")" ]] || { echo "verify-linux-packages: filename mismatch for $package" >&2; exit 1; }
    actual="$(shasum -a 256 "$package" | awk '{print $1}')"
    [[ "$expected" == "$actual" ]] || { echo "verify-linux-packages: checksum mismatch for $package" >&2; exit 1; }
  done
done
echo "verify-linux-packages: $VERSION ok"
