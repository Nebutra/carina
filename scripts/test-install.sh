#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-install-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT
version=9.8.7
stage="$work/stage/carina-$version"
mkdir -p "$stage/bin" "$work/release/v$version" "$work/bin"
printf '#!/bin/sh\nexit 0\n' > "$work/bin/carina-tui"
chmod +x "$work/bin/carina-tui"
printf '#!/bin/sh\necho %s\n' "$version" > "$stage/bin/carina"
printf '#!/bin/sh\nexit 0\n' > "$stage/bin/carina-ui"
chmod +x "$stage/bin/carina" "$stage/bin/carina-ui"
archive="$work/release/v$version/carina_${version}_linux_amd64.tar.gz"
tar -czf "$archive" -C "$work/stage" "carina-$version"
digest="$(shasum -a 256 "$archive" | awk '{print $1}')"
printf '%s  %s\n' "$digest" "$(basename "$archive")" > "$archive.sha256"

VERSION="$version" CARINA_INSTALL_OS=linux CARINA_INSTALL_ARCH=amd64 \
  CARINA_RELEASE_BASE_URL="file://$work/release/v$version" BIN_DIR="$work/bin" \
  "$ROOT/scripts/install.sh" >/dev/null
[[ "$($work/bin/carina)" == "$version" ]]
[[ -x "$work/bin/carina-ui" ]]
[[ ! -e "$work/bin/carina-tui" ]]

printf '%064d  %s\n' 0 "$(basename "$archive")" > "$archive.sha256"
if VERSION="$version" CARINA_INSTALL_OS=linux CARINA_INSTALL_ARCH=amd64 \
  CARINA_RELEASE_BASE_URL="file://$work/release/v$version" BIN_DIR="$work/bad" \
  "$ROOT/scripts/install.sh" >/dev/null 2>&1; then
  echo "test-install: bad checksum was accepted" >&2
  exit 1
fi
echo "test-install: ok"
