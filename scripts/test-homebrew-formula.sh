#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/carina-homebrew-test.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

VERSION=0.6.3 \
DARWIN_ARM64_SHA256="$(printf 'a%.0s' {1..64})" \
DARWIN_AMD64_SHA256="$(printf 'b%.0s' {1..64})" \
LINUX_ARM64_SHA256="$(printf 'c%.0s' {1..64})" \
LINUX_AMD64_SHA256="$(printf 'd%.0s' {1..64})" \
OUTPUT="$tmp/Formula/carina.rb" \
  "$ROOT/scripts/render-homebrew-formula.sh"

formula="$tmp/Formula/carina.rb"
grep -Fq 'version "0.6.3"' "$formula"
grep -Fq 'https://github.com/Nebutra/carina/releases/download/v0.6.3/' "$formula"
grep -Fq 'on_macos do' "$formula"
grep -Fq 'on_linux do' "$formula"
grep -Fq 'carina_0.6.3_darwin_arm64.tar.gz' "$formula"
grep -Fq 'carina_0.6.3_darwin_amd64.tar.gz' "$formula"
grep -Fq 'carina_0.6.3_linux_arm64.tar.gz' "$formula"
grep -Fq 'carina_0.6.3_linux_amd64.tar.gz' "$formula"
grep -Fq "$(printf 'a%.0s' {1..64})" "$formula"
grep -Fq "$(printf 'b%.0s' {1..64})" "$formula"
grep -Fq "$(printf 'c%.0s' {1..64})" "$formula"
grep -Fq "$(printf 'd%.0s' {1..64})" "$formula"
if grep -Eq '__[A-Z0-9_]+__' "$formula"; then
  printf 'test-homebrew-formula: unresolved placeholder\n' >&2
  exit 1
fi

"$ROOT/scripts/check-homebrew-version.sh" "" 0.6.3
"$ROOT/scripts/check-homebrew-version.sh" 0.6.3 0.6.3
"$ROOT/scripts/check-homebrew-version.sh" 0.6.1 0.6.3
if "$ROOT/scripts/check-homebrew-version.sh" 0.7.0 0.6.3 >/dev/null 2>&1; then
  echo "test-homebrew-formula: downgrade was accepted" >&2
  exit 1
fi

printf 'test-homebrew-formula: ok\n'
