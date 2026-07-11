#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$root"
version="${VERSION:?VERSION is required}"
[[ "$version" == "$(go run ./scripts/product-version.go)" ]]
out="$root/dist/npm"; rm -rf "$out"; mkdir -p "$out"
for tuple in darwin:arm64:arm64 darwin:amd64:x64 linux:arm64:arm64 linux:amd64:x64; do
  IFS=: read -r platform archive_arch npm_arch <<<"$tuple"
  archive="$root/dist/carina_${version}_${platform}_${archive_arch}.tar.gz"
  [[ -f "$archive" ]] || { echo "package-npm-release: missing $archive" >&2; exit 1; }
  package="@nebutra+carina-${platform}-${npm_arch}"; dir="$out/$package"; mkdir -p "$dir/bin"
  stage="$(tar -tzf "$archive" | head -n1 | cut -d/ -f1)"
  for binary in carina carina-daemon carina-worker carina-tui carina-kernel-service; do tar -xOf "$archive" "$stage/bin/$binary" > "$dir/bin/$binary"; chmod 755 "$dir/bin/$binary"; done
  sed -e "s/__PLATFORM__/$platform/g" -e "s/__ARCH__/$npm_arch/g" -e "s/__NPM_ARCH__/$npm_arch/g" -e "s/__VERSION__/$version/g" packaging/npm/platform-package.json.template > "$dir/package.json"
  (cd "$dir" && shasum -a 256 bin/* > SHA256SUMS)
  command -v syft >/dev/null || { echo "package-npm-release: syft is required" >&2; exit 127; }
  syft scan "dir:$dir/bin" -o spdx-json="$dir/SBOM.spdx.json"
  PACKAGE_DIR="$dir" python3 <<'PY'
import hashlib, json, os
from pathlib import Path
root = Path(os.environ["PACKAGE_DIR"])
subjects = [{"name": f"bin/{path.name}", "digest": {"sha256": hashlib.sha256(path.read_bytes()).hexdigest()}} for path in sorted((root / "bin").iterdir())]
statement = {"_type": "https://in-toto.io/Statement/v1", "subject": subjects, "predicateType": "https://slsa.dev/provenance/v1", "predicate": {"buildDefinition": {"buildType": "https://github.com/Nebutra/carina/.github/workflows/release.yml"}, "runDetails": {"builder": {"id": "https://github.com/actions/runner"}}}}
(root / "provenance.json").write_text(json.dumps(statement, sort_keys=True) + "\n", encoding="utf-8")
PY
  (cd "$dir" && npm pack --ignore-scripts >/dev/null)
done
launcher="$out/@nebutra+carina"; mkdir -p "$launcher/bin"; cp packaging/npm/package.json packaging/npm/README.md "$launcher/"; cp packaging/npm/bin/carina.js "$launcher/bin/"
(cd "$launcher" && npm pack --ignore-scripts >/dev/null)
echo "package-npm-release: ok"
