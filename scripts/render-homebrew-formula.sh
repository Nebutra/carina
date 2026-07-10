#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATE="${TEMPLATE:-$ROOT/packaging/homebrew/carina.rb.template}"
OUTPUT="${OUTPUT:-$ROOT/dist/homebrew/Formula/carina.rb}"

version="${VERSION:-}"
darwin_arm64_sha256="${DARWIN_ARM64_SHA256:-}"
darwin_amd64_sha256="${DARWIN_AMD64_SHA256:-}"
release_base_url="${RELEASE_BASE_URL:-https://github.com/Nebutra/carina/releases/download/v${version}}"

if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  printf 'render-homebrew-formula: VERSION must be a release version, got %q\n' "$version" >&2
  exit 2
fi

for pair in \
  "DARWIN_ARM64_SHA256:$darwin_arm64_sha256" \
  "DARWIN_AMD64_SHA256:$darwin_amd64_sha256"; do
  name="${pair%%:*}"
  value="${pair#*:}"
  if [[ ! "$value" =~ ^[0-9a-f]{64}$ ]]; then
    printf 'render-homebrew-formula: %s must be a lowercase SHA-256\n' "$name" >&2
    exit 2
  fi
done

if [[ ! "$release_base_url" =~ ^(https|file)://[^[:space:]]+$ ]]; then
  printf 'render-homebrew-formula: RELEASE_BASE_URL must be an https:// or file:// URL\n' >&2
  exit 2
fi

if [[ ! -f "$TEMPLATE" ]]; then
  printf 'render-homebrew-formula: template not found: %s\n' "$TEMPLATE" >&2
  exit 1
fi

mkdir -p "$(dirname "$OUTPUT")"
tmp="$(mktemp "${TMPDIR:-/tmp}/carina-formula.XXXXXX")"
trap 'rm -f "$tmp"' EXIT

sed \
  -e "s|__VERSION__|$version|g" \
  -e "s|__DARWIN_ARM64_SHA256__|$darwin_arm64_sha256|g" \
  -e "s|__DARWIN_AMD64_SHA256__|$darwin_amd64_sha256|g" \
  -e "s|__RELEASE_BASE_URL__|$release_base_url|g" \
  "$TEMPLATE" > "$tmp"

if grep -Eq '__[A-Z0-9_]+__' "$tmp"; then
  printf 'render-homebrew-formula: unresolved template placeholder\n' >&2
  exit 1
fi

install -m 0644 "$tmp" "$OUTPUT"
printf 'Homebrew formula: %s\n' "$OUTPUT"
