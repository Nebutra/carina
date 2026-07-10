#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/carina-homebrew-test.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

VERSION=0.6.0 \
DARWIN_ARM64_SHA256="$(printf 'a%.0s' {1..64})" \
DARWIN_AMD64_SHA256="$(printf 'b%.0s' {1..64})" \
OUTPUT="$tmp/Formula/carina.rb" \
  "$ROOT/scripts/render-homebrew-formula.sh"

formula="$tmp/Formula/carina.rb"
grep -Fq 'version "0.6.0"' "$formula"
grep -Fq 'https://github.com/Nebutra/carina/releases/download/v0.6.0/' "$formula"
grep -Fq 'carina_0.6.0_darwin_arm64.tar.gz' "$formula"
grep -Fq 'carina_0.6.0_darwin_amd64.tar.gz' "$formula"
grep -Fq "$(printf 'a%.0s' {1..64})" "$formula"
grep -Fq "$(printf 'b%.0s' {1..64})" "$formula"
if grep -Eq '__[A-Z0-9_]+__' "$formula"; then
  printf 'test-homebrew-formula: unresolved placeholder\n' >&2
  exit 1
fi

printf 'test-homebrew-formula: ok\n'
