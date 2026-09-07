#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="${DIST:-$ROOT/dist}"
VERSION="${VERSION:?VERSION is required}"
REQUIRE_TAURI="${REQUIRE_TAURI:-0}"
vsix="$DIST/carina_${VERSION}_vscode.vsix"
web="$DIST/carina_${VERSION}_web-operator.tar.gz"
installer="$DIST/carina-install.sh"

verify_checksum() {
  local artifact="$1"
  checksum="$artifact.sha256"
  [[ -f "$artifact" && -f "$checksum" ]] || { echo "verify-integration-packages: missing $artifact or checksum" >&2; exit 1; }
  read -r expected filename < "$checksum"
  [[ "$filename" == "$(basename "$artifact")" ]] || { echo "verify-integration-packages: filename mismatch" >&2; exit 1; }
  actual="$(shasum -a 256 "$artifact" | awk '{print $1}')"
  [[ "$actual" == "$expected" ]] || { echo "verify-integration-packages: checksum mismatch for $artifact" >&2; exit 1; }
}

for artifact in "$vsix" "$web" "$installer"; do
  verify_checksum "$artifact"
done
grep -Fq 'checksum mismatch' "$installer" || { echo "verify-integration-packages: installer lacks checksum enforcement" >&2; exit 1; }

python3 - "$vsix" "$web" "$VERSION" <<'PY'
import json, pathlib, posixpath, sys, tarfile, zipfile
from html.parser import HTMLParser
from urllib.parse import urlsplit


class AssetReferences(HTMLParser):
    def __init__(self):
        super().__init__()
        self.references = []

    def handle_starttag(self, tag, attrs):
        attributes = dict(attrs)
        attribute = "src" if tag in {"script", "img"} else "href" if tag == "link" else None
        if attribute and attributes.get(attribute):
            self.references.append(attributes[attribute])

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
    index_html = archive.extractfile(f"carina-web-operator-{version}/index.html").read().decode("utf-8")
prefix = f"carina-web-operator-{version}"
required = {f"{prefix}/{name}" for name in (
    "README.md", "index.html", "brand-variables.css", "logo/carina-symbol.svg",
    "logo/carina-symbol-high-contrast.svg", "logo/carina-horizontal-brand.svg",
    "logo/carina-horizontal-monochrome.svg", "logo/carina-sprite.svg", "fonts/geist-sans-latin-variable.woff2",
    "fonts/geist-mono-latin-variable.woff2",
)}
if not required.issubset(names):
    raise SystemExit(f"web operator contents missing: {sorted(required - names)}")
if not any(name.startswith(f"{prefix}/assets/") and name.endswith(".js") for name in names):
    raise SystemExit("web operator bundle lacks Vite JavaScript output")
if not any(name.startswith(f"{prefix}/assets/") and name.endswith(".css") for name in names):
    raise SystemExit("web operator bundle lacks Vite CSS output")

parser = AssetReferences()
parser.feed(index_html)
if not parser.references:
    raise SystemExit("web operator index has no packaged asset references")
for reference in parser.references:
    parsed = urlsplit(reference)
    if parsed.scheme or parsed.netloc or reference.startswith("//"):
        continue
    if parsed.path.startswith("/"):
        raise SystemExit(f"web operator asset must use a relative base: {reference}")
    resolved = posixpath.normpath(posixpath.join(prefix, parsed.path))
    if not resolved.startswith(f"{prefix}/") or resolved not in names:
        raise SystemExit(f"web operator asset does not resolve inside archive: {reference}")
PY

if [[ "$REQUIRE_TAURI" == "1" ]]; then
  tauri_artifacts=(
    "$DIST/carina-harness_${VERSION}_darwin_arm64.dmg"
    "$DIST/carina-harness_${VERSION}_darwin_amd64.dmg"
    "$DIST/carina-harness_${VERSION}_linux_arm64.AppImage"
    "$DIST/carina-harness_${VERSION}_linux_arm64.deb"
    "$DIST/carina-harness_${VERSION}_linux_amd64.AppImage"
    "$DIST/carina-harness_${VERSION}_linux_amd64.deb"
  )
  actual_count="$(find "$DIST" -maxdepth 1 -type f -name "carina-harness_${VERSION}_*" ! -name '*.sha256' | wc -l | tr -d ' ')"
  [[ "$actual_count" == "${#tauri_artifacts[@]}" ]] || {
    echo "verify-integration-packages: expected ${#tauri_artifacts[@]} Tauri bundles, found $actual_count" >&2
    exit 1
  }
  for artifact in "${tauri_artifacts[@]}"; do
    verify_checksum "$artifact"
  done
  python3 - "${tauri_artifacts[@]}" <<'PY'
import pathlib, sys

for raw in sys.argv[1:]:
    path = pathlib.Path(raw)
    data = path.read_bytes()
    if path.suffix == ".dmg" and (len(data) < 512 or data[-512:-508] != b"koly"):
        raise SystemExit(f"Tauri bundle is not a DMG: {path.name}")
    if path.suffix == ".AppImage" and not data.startswith(b"\x7fELF"):
        raise SystemExit(f"Tauri bundle is not an AppImage ELF: {path.name}")
    if path.suffix == ".deb" and not data.startswith(b"!<arch>\n"):
        raise SystemExit(f"Tauri bundle is not a Debian archive: {path.name}")
PY
fi
echo "verify-integration-packages: $VERSION ok"
