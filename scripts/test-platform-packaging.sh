#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$root"
node integrations/web/test.mjs
(cd packaging/npm && npm test)
test "$(go run ./scripts/product-version.go)" = "$(node -p "require('./packaging/npm/package.json').version")"
grep -q 'USER 65532:65532' packaging/docker/daemon.Dockerfile packaging/docker/worker.Dockerfile
grep -q 'CARINA_KERNEL_BIN=/usr/local/bin/carina-kernel-service' packaging/docker/daemon.Dockerfile
grep -q 'syft scan' scripts/generate-sbom.sh
grep -q 'nfpm package' scripts/package-linux.sh
test -f integrations/vscode/package.json
echo 'platform packaging smoke: ok'
