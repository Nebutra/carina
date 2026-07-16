#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-linux-assets.XXXXXX")"
trap 'rm -rf "$work"' EXIT
version=0.6.3
for arch in arm64 amd64; do
  for ext in deb rpm; do
    package="$work/carina_${version}_linux_${arch}.$ext"
    printf '%s-%s\n' "$arch" "$ext" > "$package"
    printf '%s  %s\n' "$(shasum -a 256 "$package" | awk '{print $1}')" "$(basename "$package")" > "$package.sha256"
  done
done
DIST="$work" VERSION="$version" "$ROOT/scripts/verify-linux-packages.sh"
printf 'corrupt\n' >> "$work/carina_${version}_linux_arm64.deb"
if DIST="$work" VERSION="$version" "$ROOT/scripts/verify-linux-packages.sh" >/dev/null 2>&1; then
  echo "test-verify-linux-packages: corrupt package was accepted" >&2
  exit 1
fi
echo "test-verify-linux-packages: ok"
