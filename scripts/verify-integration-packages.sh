#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="${DIST:-$ROOT/dist}"
VERSION="${VERSION:?VERSION is required}"
vsix="$DIST/carina_${VERSION}_vscode.vsix"
web="$DIST/carina_${VERSION}_web-operator.tar.gz"
installer="$DIST/carina-install.sh"

for artifact in "$vsix" "$web" "$installer"; do
  checksum="$artifact.sha256"
  [[ -f "$artifact" && -f "$checksum" ]] || { echo "verify-integration-packages: missing $artifact or checksum" >&2; exit 1; }
  read -r expected filename < "$checksum"
  [[ "$filename" == "$(basename "$artifact")" ]] || { echo "verify-integration-packages: filename mismatch" >&2; exit 1; }
  actual="$(shasum -a 256 "$artifact" | awk '{print $1}')"
  [[ "$actual" == "$expected" ]] || { echo "verify-integration-packages: checksum mismatch for $artifact" >&2; exit 1; }
done
grep -Fq 'checksum mismatch' "$installer" || { echo "verify-integration-packages: installer lacks checksum enforcement" >&2; exit 1; }

python3 - "$vsix" "$web" "$VERSION" <<'PY'
import json, pathlib, sys, tarfile, zipfile

vsix = pathlib.Path(sys.argv[1])
web = pathlib.Path(sys.argv[2])
version = sys.argv[3]
with zipfile.ZipFile(vsix) as archive:
    names = set(archive.namelist())
    required = {"extension/package.json", "extension/dist/extension.js", "extension/media/carina.svg"}
    missing = required - names
    if missing:
        raise SystemExit(f"VSIX missing {sorted(missing)}")
    manifest = json.loads(archive.read("extension/package.json"))
    if manifest.get("version") != version or manifest.get("publisher") != "nebutra":
        raise SystemExit("VSIX manifest version/publisher mismatch")
with tarfile.open(web, "r:gz") as archive:
    names = set(archive.getnames())
prefix = f"carina-web-operator-{version}"
required = {f"{prefix}/{name}" for name in ("README.md", "index.html", "app.css", "app.js")}
if names != required:
    raise SystemExit(f"web operator contents mismatch: {sorted(names)}")
PY
echo "verify-integration-packages: $VERSION ok"
