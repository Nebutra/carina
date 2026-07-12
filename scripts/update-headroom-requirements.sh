#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

command -v uv >/dev/null 2>&1 || {
  echo "update-headroom-requirements: uv is required" >&2
  exit 127
}

uv pip compile integrations/headroom-requirements.in \
  --universal \
  --python-version 3.12 \
  --exclude-newer '2026-07-04T00:00:00Z' \
  --generate-hashes \
  --no-emit-package headroom-ai \
  --no-annotate \
  --custom-compile-command 'scripts/update-headroom-requirements.sh' \
  --output-file integrations/headroom-requirements.lock
