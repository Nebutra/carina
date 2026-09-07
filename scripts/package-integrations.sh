#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
version="${VERSION:-$(go run ./scripts/product-version.go)}"
dist="${DIST:-$ROOT/dist}"
extension_version="$(node -p "require('./integrations/vscode/package.json').version")"
[[ "$extension_version" == "$version" ]] || {
  echo "package-integrations: VS Code version $extension_version != product version $version" >&2
  exit 1
}
mkdir -p "$dist"

# The Web Harness is also the Tauri frontend. Keep its bundled assets generated
# from the canonical brand sources before either package is assembled.
node integrations/tauri/scripts/sync-assets.mjs --web-only

(
  cd integrations/web
  npm ci
  npm test
  npm run build
)

(
  cd integrations/vscode
  npm ci
  npm test
  ./node_modules/.bin/vsce package --no-dependencies --out "$dist/carina_${version}_vscode.vsix"
)

python3 - "$ROOT" "$dist/carina_${version}_web-operator.tar.gz" "$version" <<'PY'
import gzip, io, pathlib, tarfile, sys

root = pathlib.Path(sys.argv[1])
output = pathlib.Path(sys.argv[2])
version = sys.argv[3]
dist_root = root / "integrations" / "web" / "dist"
files = {"README.md": (root / "integrations" / "web" / "README.md").read_bytes()}
for path in dist_root.rglob("*"):
    if path.is_file():
        files[str(path.relative_to(dist_root))] = path.read_bytes()
prefix = f"carina-web-operator-{version}"
with output.open("wb") as raw:
    with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as gz:
        with tarfile.open(fileobj=gz, mode="w") as archive:
            for name, data in sorted(files.items()):
                info = tarfile.TarInfo(f"{prefix}/{name}")
                info.size = len(data)
                info.mode = 0o644
                info.mtime = 0
                info.uid = info.gid = 0
                info.uname = info.gname = "root"
                archive.addfile(info, io.BytesIO(data))
PY
cp scripts/install.sh "$dist/carina-install.sh"
chmod 0755 "$dist/carina-install.sh"

for artifact in "$dist/carina_${version}_vscode.vsix" "$dist/carina_${version}_web-operator.tar.gz" "$dist/carina-install.sh"; do
  digest="$(shasum -a 256 "$artifact" | awk '{print $1}')"
  printf '%s  %s\n' "$digest" "$(basename "$artifact")" > "$artifact.sha256"
done
DIST="$dist" VERSION="$version" ./scripts/verify-integration-packages.sh
echo "package-integrations: packaged VS Code, web operator, and installer assets"
