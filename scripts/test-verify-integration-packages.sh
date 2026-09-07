#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-integration-package-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT
version="$(go run "$ROOT/scripts/product-version.go")"

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
            for name in (
                "README.md", "index.html", "assets/index-test.js", "assets/index-test.css",
                "brand-variables.css", "logo/carina-symbol.svg",
                "logo/carina-symbol-high-contrast.svg",
                "logo/carina-horizontal-brand.svg", "logo/carina-horizontal-monochrome.svg", "logo/carina-sprite.svg",
                "fonts/geist-sans-latin-variable.woff2", "fonts/geist-mono-latin-variable.woff2",
            ):
                data = (
                    b'<link rel="stylesheet" href="./assets/index-test.css">'
                    b'<script type="module" src="./assets/index-test.js"></script>'
                    if name == "index.html" else b"test\n"
                )
                info = tarfile.TarInfo(f"carina-web-operator-{version}/{name}")
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))

formats = {
    f"carina-harness_{version}_darwin_arm64.dmg": b"koly" + bytes(508),
    f"carina-harness_{version}_darwin_amd64.dmg": b"koly" + bytes(508),
    f"carina-harness_{version}_linux_arm64.AppImage": b"\x7fELFfixture",
    f"carina-harness_{version}_linux_arm64.deb": b"!<arch>\nfixture",
    f"carina-harness_{version}_linux_amd64.AppImage": b"\x7fELFfixture",
    f"carina-harness_{version}_linux_amd64.deb": b"!<arch>\nfixture",
}
for name, data in formats.items():
    if name.endswith(".dmg"):
        data = bytes(512) + data
    (root / name).write_bytes(data)
PY
printf '#!/bin/sh\necho "checksum mismatch"\n' > "$work/carina-install.sh"
chmod +x "$work/carina-install.sh"
for artifact in "$work"/*; do
  digest="$(shasum -a 256 "$artifact" | awk '{print $1}')"
  printf '%s  %s\n' "$digest" "$(basename "$artifact")" > "$artifact.sha256"
done
DIST="$work" VERSION="$version" REQUIRE_TAURI=1 "$ROOT/scripts/verify-integration-packages.sh" >/dev/null
web="$work/carina_${version}_web-operator.tar.gz"
cp "$web" "$work/web.backup"
python3 - "$web" "$version" <<'PY'
import gzip, io, pathlib, tarfile, sys

output = pathlib.Path(sys.argv[1])
version = sys.argv[2]
prefix = f"carina-web-operator-{version}"
files = {
    f"{prefix}/README.md": b"test\n",
    f"{prefix}/index.html": b'<script type="module" src="/integrations/web/assets/index-test.js"></script>',
    f"{prefix}/assets/index-test.js": b"test\n",
    f"{prefix}/assets/index-test.css": b"test\n",
}
for name in (
    "brand-variables.css", "logo/carina-symbol.svg", "logo/carina-symbol-high-contrast.svg",
    "logo/carina-horizontal-brand.svg", "logo/carina-horizontal-monochrome.svg", "logo/carina-sprite.svg",
    "fonts/geist-sans-latin-variable.woff2", "fonts/geist-mono-latin-variable.woff2",
):
    files[f"{prefix}/{name}"] = b"test\n"
with output.open("wb") as raw:
    with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as gz:
        with tarfile.open(fileobj=gz, mode="w") as archive:
            for name, data in sorted(files.items()):
                info = tarfile.TarInfo(name)
                info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
PY
digest="$(shasum -a 256 "$web" | awk '{print $1}')"
printf '%s  %s\n' "$digest" "$(basename "$web")" > "$web.sha256"
if DIST="$work" VERSION="$version" "$ROOT/scripts/verify-integration-packages.sh" >/dev/null 2>&1; then
  echo "test-verify-integration-packages: absolute Web asset base was accepted" >&2
  exit 1
fi
mv "$work/web.backup" "$web"
digest="$(shasum -a 256 "$web" | awk '{print $1}')"
printf '%s  %s\n' "$digest" "$(basename "$web")" > "$web.sha256"

cp "$work/carina_${version}_vscode.vsix" "$work/vscode.backup"
printf 'corrupt\n' >> "$work/carina_${version}_vscode.vsix"
if DIST="$work" VERSION="$version" "$ROOT/scripts/verify-integration-packages.sh" >/dev/null 2>&1; then
  echo "test-verify-integration-packages: corruption was accepted" >&2
  exit 1
fi
mv "$work/vscode.backup" "$work/carina_${version}_vscode.vsix"

tauri="$work/carina-harness_${version}_linux_amd64.AppImage"
printf 'not-elf\n' > "$tauri"
digest="$(shasum -a 256 "$tauri" | awk '{print $1}')"
printf '%s  %s\n' "$digest" "$(basename "$tauri")" > "$tauri.sha256"
if DIST="$work" VERSION="$version" REQUIRE_TAURI=1 "$ROOT/scripts/verify-integration-packages.sh" >/dev/null 2>&1; then
  echo "test-verify-integration-packages: invalid Tauri bundle was accepted" >&2
  exit 1
fi
echo "test-verify-integration-packages: ok"
