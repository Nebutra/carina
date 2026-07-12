#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-assets-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT
version="9.8.7"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}

for target in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
  archive="$work/carina_${version}_${target}.tar.gz"
  printf '%s\n' "$target" > "$archive"
  printf '%s  %s\n' "$(sha256_file "$archive")" "$(basename "$archive")" > "$archive.sha256"
done
for arch in arm64 amd64; do
  archive="$work/carina_${version}_darwin_${arch}.tar.gz"
  printf '{"status":"Accepted","id":"submission-%s"}\n' "$arch" > "$archive.notary.json"
  printf 'verified %s\n' "$arch" > "$archive.signing.txt"
done

DIST="$work" VERSION="$version" "$ROOT/scripts/verify-release-assets.sh"
[[ "$(wc -l < "$work/SHA256SUMS" | tr -d ' ')" == "4" ]]

archive="$work/carina_${version}_linux_arm64.tar.gz"
checksum="$archive.sha256"
original_checksum="$(cat "$checksum")"
printf '%064d  %s\n' 0 "$(basename "$archive")" > "$checksum"
if DIST="$work" VERSION="$version" "$ROOT/scripts/verify-release-assets.sh" >/dev/null 2>&1; then
  echo "test-verify-release-assets: bad checksum was accepted" >&2
  exit 1
fi
printf '%s\n' "$original_checksum" > "$checksum"

digest="${original_checksum%% *}"
printf '%s  wrong-name.tar.gz\n' "$digest" > "$checksum"
if DIST="$work" VERSION="$version" "$ROOT/scripts/verify-release-assets.sh" >/dev/null 2>&1; then
  echo "test-verify-release-assets: checksum filename mismatch was accepted" >&2
  exit 1
fi
printf '%s\n' "$original_checksum" > "$checksum"

notary="$work/carina_${version}_darwin_arm64.tar.gz.notary.json"
printf '{"status":"Rejected","id":"submission-arm64"}\n' > "$notary"
if DIST="$work" VERSION="$version" "$ROOT/scripts/verify-release-assets.sh" >/dev/null 2>&1; then
  echo "test-verify-release-assets: rejected notarization was accepted" >&2
  exit 1
fi
printf '{"status":"Accepted"}\n' > "$notary"
if DIST="$work" VERSION="$version" "$ROOT/scripts/verify-release-assets.sh" >/dev/null 2>&1; then
  echo "test-verify-release-assets: notarization without id was accepted" >&2
  exit 1
fi
printf '{not-json\n' > "$notary"
if DIST="$work" VERSION="$version" "$ROOT/scripts/verify-release-assets.sh" >/dev/null 2>&1; then
  echo "test-verify-release-assets: corrupt notarization was accepted" >&2
  exit 1
fi
printf '{"status":"Accepted","id":"submission-arm64"}\n' > "$notary"

extra="$work/carina_${version}_linux_s390x.tar.gz"
printf 'extra\n' > "$extra"
if DIST="$work" VERSION="$version" "$ROOT/scripts/verify-release-assets.sh" >/dev/null 2>&1; then
  echo "test-verify-release-assets: extra archive was accepted" >&2
  exit 1
fi
rm "$extra"

rm "$archive"
if DIST="$work" VERSION="$version" "$ROOT/scripts/verify-release-assets.sh" >/dev/null 2>&1; then
  echo "test-verify-release-assets: missing archive was accepted" >&2
  exit 1
fi
echo "test-verify-release-assets: ok"
