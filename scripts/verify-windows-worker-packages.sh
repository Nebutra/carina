#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="${DIST:-$ROOT/dist}"
VERSION="${VERSION:?VERSION is required}"
for arch in amd64 arm64; do
  archive="$DIST/carina-worker_${VERSION}_windows_${arch}.zip"
  checksum="$archive.sha256"
  [[ -f "$archive" && -f "$checksum" ]] || { echo "verify-windows-worker-packages: missing $archive or checksum" >&2; exit 1; }
  read -r expected filename < "$checksum"
  [[ "$filename" == "$(basename "$archive")" ]] || { echo "verify-windows-worker-packages: filename mismatch" >&2; exit 1; }
  [[ "$expected" == "$(shasum -a 256 "$archive" | awk '{print $1}')" ]] || { echo "verify-windows-worker-packages: checksum mismatch" >&2; exit 1; }
  python3 - "$archive" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1]) as z:
    names = z.namelist()
    assert sum(name.endswith('/bin/carina-worker.exe') for name in names) == 1
    assert sum(name.endswith('/README.md') for name in names) == 1
    assert sum(name.endswith('/LICENSE') for name in names) == 1
PY
done
echo "verify-windows-worker-packages: $VERSION ok"
