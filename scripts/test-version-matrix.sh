#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
version="$(go run ./scripts/product-version.go)"
[[ "$version" == "0.6.5" ]] || { printf 'version-matrix: product=%s want=0.6.5\n' "$version" >&2; exit 1; }
node -e 'const p=require("./packaging/npm/package.json"); if(p.version!==process.argv[1]||Object.values(p.optionalDependencies).some(v=>v!==process.argv[1])) process.exit(1)' "$version"
grep -Fq "VERSION=$version" scripts/test-homebrew-formula.sh
grep -Fq "VERSION=$version make release-package" docs/release.md
grep -Fq 'required="0.15.1"' scripts/zig-tool.sh
grep -Fq 'version="0.15.1"' scripts/install-zig-tool.sh
test "$(grep -c 'version: 0.15.1' .github/workflows/release.yml)" -eq 3
grep -Fq './scripts/build-zig-tools.sh' Makefile
grep -Fq './scripts/build-zig-tools.sh' .github/workflows/ci.yml
grep -Fq '"$ROOT/scripts/build-zig-tools.sh"' scripts/package-release.sh
if grep -Eq '(^|[[:space:]])zig build([[:space:]]|$)' Makefile scripts/package-release.sh .github/workflows/ci.yml; then
  echo "version-matrix: a product build path bypasses the pinned Zig builder" >&2
  exit 1
fi
if grep -Eq 'uses: [^ ]+@(v[0-9]+|stable)([[:space:]]|$)' .github/workflows/ci.yml .github/workflows/release.yml; then
  echo "version-matrix: release workflows contain mutable action refs" >&2
  exit 1
fi
grep -Fq 'syft-version: v1.46.0' .github/workflows/release.yml
grep -Fq 'freeze-npm:' .github/workflows/release.yml
grep -Fq './scripts/package-npm-bundle.sh' .github/workflows/release.yml
grep -Fq './scripts/verify-npm-release-bundle.sh' .github/workflows/release.yml
grep -Fq 'group: carina-homebrew-tap-main' .github/workflows/release.yml
grep -Fq 'scripts/check-homebrew-version.sh' .github/workflows/release.yml
checkout_count="$(grep -hc 'uses: actions/checkout@' .github/workflows/ci.yml .github/workflows/release.yml | awk '{s+=$1} END {print s}')"
persist_count="$(grep -hc 'persist-credentials: false' .github/workflows/ci.yml .github/workflows/release.yml | awk '{s+=$1} END {print s}')"
[[ "$checkout_count" == "$persist_count" ]] || { echo "version-matrix: every checkout must disable credential persistence" >&2; exit 1; }
printf 'version-matrix: %s ok\n' "$version"
