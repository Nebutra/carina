#!/usr/bin/env bash
set -euo pipefail

required="0.15.1"
zig_bin="${CARINA_ZIG_BIN:-}"
if [[ -n "$zig_bin" ]]; then
  if [[ ! -x "$zig_bin" ]]; then
    echo "zig-tool: CARINA_ZIG_BIN is not executable: ${zig_bin}" >&2
    exit 127
  fi
  version="$($zig_bin version)"
  if [[ "$version" != "$required" ]]; then
    echo "zig-tool: unsupported Zig ${version}; required ${required} (${zig_bin})" >&2
    exit 127
  fi
else
  zig_bin="$(command -v zig || true)"
  if [[ -n "$zig_bin" && "$($zig_bin version)" != "$required" ]]; then
    zig_bin=""
  fi
  if [[ -z "$zig_bin" ]]; then
    if [[ "${CARINA_ZIG_AUTO_INSTALL:-1}" != "1" ]]; then
      echo "zig-tool: Zig ${required} is unavailable and automatic installation is disabled" >&2
      exit 127
    fi
    zig_bin="$("$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/install-zig-tool.sh")"
  fi
fi
exec "$zig_bin" "$@"
