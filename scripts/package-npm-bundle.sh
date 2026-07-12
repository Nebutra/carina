#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:?VERSION is required}"
DIST="${DIST_DIR:-$ROOT/dist}"
NPM_DIR="${NPM_DIR:-$DIST/npm}"
BUNDLE="${BUNDLE:-$DIST/carina_${VERSION}_npm_packages.tar.gz}"

expected=(
  "nebutra-carina-${VERSION}.tgz"
  "nebutra-carina-darwin-arm64-${VERSION}.tgz"
  "nebutra-carina-darwin-x64-${VERSION}.tgz"
  "nebutra-carina-linux-arm64-${VERSION}.tgz"
  "nebutra-carina-linux-x64-${VERSION}.tgz"
)

work="$(mktemp -d "${TMPDIR:-/tmp}/carina-npm-bundle.XXXXXX")"
tmp_bundle="$BUNDLE.tmp.$$"
cleanup() { rm -rf "$work" "$tmp_bundle"; }
trap cleanup EXIT
mkdir -p "$work/npm" "$(dirname "$BUNDLE")"

for name in "${expected[@]}"; do
  source="$(find "$NPM_DIR" -type f -name "$name" -print -quit)"
  [[ -n "$source" ]] || { echo "package-npm-bundle: missing $name" >&2; exit 1; }
  cp "$source" "$work/npm/$name"
done
[[ "$(find "$NPM_DIR" -type f -name '*.tgz' | wc -l | tr -d ' ')" == "5" ]] || {
  echo "package-npm-bundle: expected exactly five npm tarballs" >&2
  exit 1
}
(cd "$work/npm" && shasum -a 256 "${expected[@]}" > SHA256SUMS)

WORK="$work" OUTPUT="$tmp_bundle" python3 <<'PY'
import gzip
import io
import os
import tarfile
from pathlib import Path

root = Path(os.environ["WORK"])
output = Path(os.environ["OUTPUT"])
entries = [root / "npm" / "SHA256SUMS", *sorted((root / "npm").glob("*.tgz"))]
with output.open("wb") as raw:
    with gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as zipped:
        with tarfile.open(fileobj=zipped, mode="w") as archive:
            for path in entries:
                data = path.read_bytes()
                info = tarfile.TarInfo(f"npm/{path.name}")
                info.size = len(data)
                info.mode = 0o644
                info.uid = info.gid = 0
                info.uname = info.gname = "root"
                info.mtime = 0
                archive.addfile(info, io.BytesIO(data))
PY
mv "$tmp_bundle" "$BUNDLE"
echo "package-npm-bundle: $BUNDLE"
