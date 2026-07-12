#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="${DIST:-$ROOT/dist}"
VERSION="${VERSION:?VERSION is required}"
REQUIRE_SIGNING="${REQUIRE_SIGNING:-1}"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

archives=()
native_archive_count="$(find "$DIST" -maxdepth 1 -type f -name "carina_${VERSION}_*.tar.gz" \
  | grep -Ec "/carina_${VERSION}_(darwin|linux)_[^.]+\.tar\.gz$" || true)"
native_checksum_count="$(find "$DIST" -maxdepth 1 -type f -name "carina_${VERSION}_*.tar.gz.sha256" \
  | grep -Ec "/carina_${VERSION}_(darwin|linux)_[^.]+\.tar\.gz\.sha256$" || true)"
[[ "$native_archive_count" == "4" ]] || {
  echo "verify-release-assets: expected exactly four archives for $VERSION" >&2
  exit 1
}
[[ "$native_checksum_count" == "4" ]] || {
  echo "verify-release-assets: expected exactly four archive checksums for $VERSION" >&2
  exit 1
}
for target in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
  archive="$DIST/carina_${VERSION}_${target}.tar.gz"
  checksum="$archive.sha256"
  [[ -f "$archive" ]] || { echo "verify-release-assets: missing $archive" >&2; exit 1; }
  [[ -f "$checksum" ]] || { echo "verify-release-assets: missing $checksum" >&2; exit 1; }
  read -r expected filename < "$checksum"
  [[ "$filename" == "$(basename "$archive")" ]] || { echo "verify-release-assets: checksum filename mismatch for $archive" >&2; exit 1; }
  [[ "$expected" == "$(sha256_file "$archive")" ]] || { echo "verify-release-assets: checksum mismatch for $archive" >&2; exit 1; }
  archives+=("$archive")
done

if [[ "$REQUIRE_SIGNING" == "1" ]]; then
  [[ "$(find "$DIST" -maxdepth 1 -type f -name "carina_${VERSION}_darwin_*.tar.gz.notary.json" | wc -l | tr -d ' ')" == "2" ]] || {
    echo "verify-release-assets: expected exactly two Darwin notary results" >&2
    exit 1
  }
  [[ "$(find "$DIST" -maxdepth 1 -type f -name "carina_${VERSION}_darwin_*.tar.gz.signing.txt" | wc -l | tr -d ' ')" == "2" ]] || {
    echo "verify-release-assets: expected exactly two Darwin signing reports" >&2
    exit 1
  }
  DIST="$DIST" VERSION="$VERSION" "$ROOT/scripts/verify-notary-evidence.sh" >/dev/null
fi

manifest="$DIST/SHA256SUMS"
: > "$manifest"
for archive in "${archives[@]}"; do
  printf '%s  %s\n' "$(sha256_file "$archive")" "$(basename "$archive")" >> "$manifest"
done
LC_ALL=C sort -k2 -o "$manifest" "$manifest"
[[ "$(wc -l < "$manifest" | tr -d ' ')" == "4" ]]
echo "verify-release-assets: $VERSION ok"
