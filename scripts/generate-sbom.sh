#!/usr/bin/env bash
set -euo pipefail
input="${1:?artifact path required}"; output="${2:?output path required}"
if ! command -v syft >/dev/null; then echo 'syft is required to generate a release SBOM' >&2; exit 127; fi
syft scan "$input" -o "spdx-json=$output"; test -s "$output"
