#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="${DIST:-$ROOT/dist}"
VERSION="${VERSION:?VERSION is required}"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}

[[ "$(find "$DIST" -maxdepth 1 -type f -name "carina_${VERSION}_darwin_*.tar.gz" | wc -l | tr -d ' ')" == "2" ]] || {
  echo "verify-notary-evidence: expected exactly two Darwin archives" >&2
  exit 1
}
for arch in arm64 amd64; do
  archive="$DIST/carina_${VERSION}_darwin_${arch}.tar.gz"
  checksum="$archive.sha256"
  notary="$archive.notary.json"
  report="$archive.signing.txt"
  [[ -f "$archive" && -s "$checksum" && -s "$notary" && -s "$report" ]] || {
    echo "verify-notary-evidence: incomplete evidence for darwin/$arch" >&2
    exit 1
  }
  read -r expected filename < "$checksum"
  [[ "$filename" == "$(basename "$archive")" && "$expected" == "$(sha256_file "$archive")" ]] || {
    echo "verify-notary-evidence: checksum mismatch for darwin/$arch" >&2
    exit 1
  }
  python3 - "$notary" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    result = json.load(handle)
if result.get("status") != "Accepted" or not result.get("id"):
    raise SystemExit(f"verify-notary-evidence: notarization is not Accepted: {sys.argv[1]}")
PY
done
echo "verify-notary-evidence: $VERSION ok"
