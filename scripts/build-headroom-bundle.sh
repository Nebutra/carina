#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOCK="${HEADROOM_LOCK:-$ROOT/integrations/headroom.lock}"
REQUIREMENTS="${HEADROOM_REQUIREMENTS_LOCK:-$ROOT/integrations/headroom-requirements.lock}"
PYTHON="${PYTHON:-}"

if [[ -z "$PYTHON" ]]; then
  for candidate in python3 python3.12; do
    if command -v "$candidate" >/dev/null 2>&1 && \
      [[ "$("$candidate" -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")')" == "3.12" ]]; then
      PYTHON="$candidate"
      break
    fi
  done
fi
if [[ -z "$PYTHON" ]] && command -v uv >/dev/null 2>&1; then
  PYTHON="$(uv python find 3.12 2>/dev/null || true)"
fi

host_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$host_os" in darwin|linux) ;; *) echo "build-headroom-bundle: unsupported host OS: $host_os" >&2; exit 2 ;; esac
host_arch="$(uname -m)"
case "$host_arch" in x86_64|amd64) host_arch=amd64 ;; arm64|aarch64) host_arch=arm64 ;; *) echo "build-headroom-bundle: unsupported host architecture: $host_arch" >&2; exit 2 ;; esac
host_target="${host_os}-${host_arch}"
target="${HEADROOM_TARGET:-$host_target}"
if [[ "$target" != "$host_target" ]]; then
  echo "build-headroom-bundle: PyInstaller cannot cross-compile $target on $host_target" >&2
  exit 2
fi

output="${OUTPUT:-$ROOT/dist/headroom/$target/headroom}"
for file in "$LOCK" "$REQUIREMENTS" "$ROOT/scripts/verify-headroom-bundle.py"; do
  [[ -f "$file" ]] || { echo "build-headroom-bundle: missing required file: $file" >&2; exit 1; }
done
[[ -n "$PYTHON" ]] && command -v "$PYTHON" >/dev/null 2>&1 || { echo "build-headroom-bundle: Python 3.12 is required" >&2; exit 127; }
command -v curl >/dev/null 2>&1 || { echo "build-headroom-bundle: curl is required" >&2; exit 127; }

python_version="$($PYTHON -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")')"
if [[ "$python_version" != "3.12" ]]; then
  echo "build-headroom-bundle: Python 3.12 is required, got $python_version" >&2
  exit 2
fi

lock_field() {
  "$PYTHON" - "$LOCK" "$target" "$1" <<'PY'
import sys, tomllib
with open(sys.argv[1], "rb") as handle:
    data = tomllib.load(handle)
value = data["artifacts"][sys.argv[2]][sys.argv[3]]
print(value)
PY
}

source_url="$(lock_field source_url)"
source_sha256="$(lock_field source_sha256)"
source_name="$(basename "${source_url%%\?*}")"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-headroom.XXXXXX")"
trap 'rm -rf "$work"' EXIT
source_file="$work/$source_name"

if [[ -n "${HEADROOM_SOURCE_FILE:-}" ]]; then
  cp "$HEADROOM_SOURCE_FILE" "$source_file"
else
  curl --fail --location --silent --show-error --retry 3 \
    --proto '=https' --tlsv1.2 "$source_url" --output "$source_file"
fi
actual_source_sha256="$($PYTHON - "$source_file" <<'PY'
import hashlib, sys
digest = hashlib.sha256()
with open(sys.argv[1], "rb") as handle:
    for chunk in iter(lambda: handle.read(1024 * 1024), b""):
        digest.update(chunk)
print(digest.hexdigest())
PY
)"
if [[ "$actual_source_sha256" != "$source_sha256" ]]; then
  echo "build-headroom-bundle: source sha256 mismatch for $source_name" >&2
  echo "  want: $source_sha256" >&2
  echo "   got: $actual_source_sha256" >&2
  exit 1
fi

"$PYTHON" -m venv "$work/venv"
venv_python="$work/venv/bin/python"
"$venv_python" -m pip install --disable-pip-version-check --require-hashes -r "$REQUIREMENTS"
install_args=(--disable-pip-version-check --no-deps)
if [[ "$source_name" == *.tar.gz ]]; then
  install_args+=(--no-build-isolation)
fi
"$venv_python" -m pip install "${install_args[@]}" "$source_file"

cat > "$work/main.py" <<'PY'
from headroom.cli import main

main()
PY

mkdir -p "$(dirname "$output")"
LITELLM_LOCAL_MODEL_COST_MAP=true PYINSTALLER_CONFIG_DIR="$work/pyinstaller" "$venv_python" -m PyInstaller \
  --noconfirm --clean --onefile --name headroom \
  --distpath "$work/dist" --workpath "$work/build" --specpath "$work" \
  --collect-all headroom --copy-metadata headroom-ai "$work/main.py"

candidate="$work/dist/headroom"
"$candidate" --help >/dev/null
mkdir -p "$work/state"
HEADROOM_WORKSPACE_DIR="$work/state" \
HEADROOM_CCR_SQLITE_PATH="$work/state/ccr_store.db" \
  "$PYTHON" "$ROOT/scripts/verify-headroom-bundle.py" "$candidate"
cp "$candidate" "$output.tmp.$$"
chmod 755 "$output.tmp.$$"
mv "$output.tmp.$$" "$output"

bundle_sha256="$($PYTHON - "$output" <<'PY'
import hashlib, sys
print(hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())
PY
)"
printf 'headroom bundle: %s\nsha256: %s\n' "$output" "$bundle_sha256"
