#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
version="$(go run ./scripts/product-version.go)"
[[ "$version" == "0.6.2" ]] || { printf 'version-matrix: product=%s want=0.6.2\n' "$version" >&2; exit 1; }
node -e 'const p=require("./packaging/npm/package.json"); if(p.version!==process.argv[1]||Object.values(p.optionalDependencies).some(v=>v!==process.argv[1])) process.exit(1)' "$version"
grep -Fq "VERSION=$version" scripts/test-homebrew-formula.sh
grep -Fq "VERSION=$version make release-package" docs/release.md
grep -Fq 'required="0.15.1"' scripts/zig-tool.sh
grep -Fq 'version="0.15.1"' scripts/install-zig-tool.sh
test "$(grep -c 'version: 0.15.1' .github/workflows/release.yml)" -eq 3
printf 'version-matrix: %s ok\n' "$version"
