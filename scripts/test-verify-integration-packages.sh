#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-integration-package-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT
version="0.6.3"

python3 - "$work" "$version" <<'PY'
import gzip, io, json, pathlib, tarfile, sys, zipfile
root, version = pathlib.Path(sys.argv[1]), sys.argv[2]
with zipfile.ZipFile(root / f"carina_{version}_vscode.vsix", "w") as z:
    z.writestr("extension/package.json", json.dumps({"version": version, "publisher": "nebutra"}))
    z.writestr("extension/dist/extension.js", "")
    z.writestr("extension/media/carina.svg", "")
with (root / f"carina_{version}_web-operator.tar.gz").open("wb") as raw:
    with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as gz:
        with tarfile.open(fileobj=gz, mode="w") as tar:
            for name in ("README.md", "index.html", "app.css", "app.js"):
                data = b"test\n"
                info = tarfile.TarInfo(f"carina-web-operator-{version}/{name}")
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
PY
printf '#!/bin/sh\necho "checksum mismatch"\n' > "$work/carina-install.sh"
chmod +x "$work/carina-install.sh"
for artifact in "$work"/*; do
  digest="$(shasum -a 256 "$artifact" | awk '{print $1}')"
  printf '%s  %s\n' "$digest" "$(basename "$artifact")" > "$artifact.sha256"
done
DIST="$work" VERSION="$version" "$ROOT/scripts/verify-integration-packages.sh" >/dev/null
printf 'corrupt\n' >> "$work/carina_${version}_vscode.vsix"
if DIST="$work" VERSION="$version" "$ROOT/scripts/verify-integration-packages.sh" >/dev/null 2>&1; then
  echo "test-verify-integration-packages: corruption was accepted" >&2
  exit 1
fi
echo "test-verify-integration-packages: ok"
