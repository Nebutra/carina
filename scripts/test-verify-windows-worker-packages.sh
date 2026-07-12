#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-windows-assets.XXXXXX")"
trap 'rm -rf "$work"' EXIT
version=0.6.2
for arch in amd64 arm64; do
  ARCHIVE="$work/carina-worker_${version}_windows_${arch}.zip" ARCH="$arch" python3 - <<'PY'
import os, zipfile
root=f'carina-worker_0.6.2_windows_{os.environ["ARCH"]}'
with zipfile.ZipFile(os.environ["ARCHIVE"], 'w') as z:
    z.writestr(root+'/bin/carina-worker.exe', b'MZ')
    z.writestr(root+'/README.md', b'docs')
    z.writestr(root+'/LICENSE', b'MIT')
PY
  archive="$work/carina-worker_${version}_windows_${arch}.zip"
  printf '%s  %s\n' "$(shasum -a 256 "$archive" | awk '{print $1}')" "$(basename "$archive")" > "$archive.sha256"
done
DIST="$work" VERSION="$version" "$ROOT/scripts/verify-windows-worker-packages.sh"
printf 'corrupt\n' >> "$work/carina-worker_${version}_windows_amd64.zip"
if DIST="$work" VERSION="$version" "$ROOT/scripts/verify-windows-worker-packages.sh" >/dev/null 2>&1; then
  echo "test-verify-windows-worker-packages: corrupt archive was accepted" >&2
  exit 1
fi
echo "test-verify-windows-worker-packages: ok"
