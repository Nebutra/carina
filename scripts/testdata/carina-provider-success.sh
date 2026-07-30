#!/usr/bin/env bash
set -euo pipefail

[[ "${1:-}" == "auth" && "${2:-}" == "login" && "${3:-}" == "test" && "${4:-}" == "-" ]] || exit 64
IFS= read -r credential || true
[[ -n "$credential" ]] || exit 65
: "${CARINA_PROVIDER_READY_FILE:?}"
printf 'ready\n' > "$CARINA_PROVIDER_READY_FILE"
