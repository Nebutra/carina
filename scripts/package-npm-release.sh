#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$root"
version="${VERSION:?VERSION is required}"
[[ "$version" == "$(go run ./scripts/product-version.go)" ]]
dist="${DIST_DIR:-$root/dist}"
out="$dist/npm"; rm -rf "$out"; mkdir -p "$out"
sha256_manifest() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}
for tuple in darwin:arm64:arm64 darwin:amd64:x64 linux:arm64:arm64 linux:amd64:x64; do
  IFS=: read -r platform archive_arch npm_arch <<<"$tuple"
  archive="$dist/carina_${version}_${platform}_${archive_arch}.tar.gz"
  [[ -f "$archive" ]] || { echo "package-npm-release: missing $archive" >&2; exit 1; }
  package="@nebutra+carina-${platform}-${npm_arch}"; dir="$out/$package"; mkdir -p "$dir/bin"
  stage="$(basename "$archive" .tar.gz)"
  for binary in \
    carina carina-daemon carina-worker carina-tui carina-kernel-service \
    carina-scan carina-grep carina-diff carina-patch-native carina-run carina-pty headroom; do
    tar -xOf "$archive" "$stage/bin/$binary" > "$dir/bin/$binary"
    chmod 755 "$dir/bin/$binary"
  done
  sed -e "s/__PLATFORM__/$platform/g" -e "s/__ARCH__/$npm_arch/g" -e "s/__NPM_ARCH__/$npm_arch/g" -e "s/__VERSION__/$version/g" packaging/npm/platform-package.json.template > "$dir/package.json"
  (cd "$dir" && sha256_manifest bin/* > SHA256SUMS)
  command -v syft >/dev/null || { echo "package-npm-release: syft is required" >&2; exit 127; }
  syft scan "dir:$dir/bin" -o spdx-json="$dir/SBOM.spdx.json"
  SBOM="$dir/SBOM.spdx.json" SBOM_NAMESPACE="https://github.com/Nebutra/carina/releases/download/v${version}/sbom/npm/${platform}-${npm_arch}" python3 <<'PY'
import json, os
from pathlib import Path

path = Path(os.environ["SBOM"])
data = json.loads(path.read_text(encoding="utf-8"))
data["documentNamespace"] = os.environ["SBOM_NAMESPACE"]
creation = data.setdefault("creationInfo", {})
creation["created"] = "1970-01-01T00:00:00Z"
path.write_text(json.dumps(data, sort_keys=True, separators=(",", ":")) + "\n", encoding="utf-8")
PY
  (cd "$dir" && npm pack --ignore-scripts >/dev/null)
done
launcher="$out/@nebutra+carina"; mkdir -p "$launcher/bin"; cp packaging/npm/package.json packaging/npm/README.md "$launcher/"; cp packaging/npm/bin/carina.js "$launcher/bin/"
(cd "$launcher" && npm pack --ignore-scripts >/dev/null)
echo "package-npm-release: ok"
