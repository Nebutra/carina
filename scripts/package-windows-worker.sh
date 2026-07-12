#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="${VERSION:-$(cd "$ROOT" && go run ./scripts/product-version.go)}"
arch="${GOARCH:-amd64}"
dist="${DIST:-$ROOT/dist}"
case "$arch" in amd64|arm64) ;; *) echo "package-windows-worker: unsupported architecture: $arch" >&2; exit 2 ;; esac

work="$(mktemp -d "${TMPDIR:-/tmp}/carina-windows-worker.XXXXXX")"
trap 'rm -rf "$work"' EXIT
stage="$work/carina-worker_${version}_windows_${arch}"
mkdir -p "$stage/bin" "$dist"
CGO_ENABLED=0 GOOS=windows GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$stage/bin/carina-worker.exe" "$ROOT/apps/carina-worker"
cp "$ROOT/LICENSE" "$stage/LICENSE"
cp "$ROOT/docs/worker-executor.md" "$stage/README.md"
archive="$dist/carina-worker_${version}_windows_${arch}.zip"
STAGE="$stage" ARCHIVE="$archive" python3 - <<'PY'
import os, pathlib, zipfile
stage = pathlib.Path(os.environ["STAGE"])
archive = pathlib.Path(os.environ["ARCHIVE"])
root = stage.name
with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as out:
    for path in sorted(p for p in stage.rglob("*") if p.is_file()):
        info = zipfile.ZipInfo(f"{root}/{path.relative_to(stage).as_posix()}", (2026, 1, 1, 0, 0, 0))
        info.create_system = 3
        info.external_attr = (0o755 if path.suffix == ".exe" else 0o644) << 16
        out.writestr(info, path.read_bytes(), compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)
PY
printf '%s  %s\n' "$(shasum -a 256 "$archive" | awk '{print $1}')" "$(basename "$archive")" > "$archive.sha256"
echo "package-windows-worker: $archive"
