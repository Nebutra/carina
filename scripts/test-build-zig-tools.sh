#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-zig-build-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT
log="$work/zig.log"
fake="$work/zig"

cat > "$fake" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "version" ]]; then
  echo 0.15.1
  exit 0
fi
printf '%q ' "$@" >> "${CARINA_ZIG_TEST_LOG:?}"
printf '\n' >> "$CARINA_ZIG_TEST_LOG"
output=""
for arg in "$@"; do
  case "$arg" in
    -femit-bin=*) output="${arg#-femit-bin=}" ;;
  esac
done
[[ -n "$output" ]]
mkdir -p "$(dirname "$output")"
printf '#!/bin/sh\nexit 0\n' > "$output"
chmod +x "$output"
SH
chmod +x "$fake"

CARINA_ZIG_BIN="$fake" \
CARINA_ZIG_TARGET="aarch64-macos.13.0" \
CARINA_ZIG_OUTPUT_DIR="$work/out" \
CARINA_ZIG_ALLOW_CUSTOM_OUTPUT=1 \
CARINA_ZIG_TEST_LOG="$log" \
  "$ROOT/scripts/build-zig-tools.sh" >/dev/null

[[ "$(find "$work/out" -maxdepth 1 -type f | wc -l | tr -d ' ')" == "6" ]]
[[ "$(wc -l < "$log" | tr -d ' ')" == "6" ]]
[[ "$(grep -c -- '-target aarch64-macos.13.0' "$log")" == "6" ]]
[[ "$(grep -c -- '-lc' "$log")" == "1" ]]
grep -F 'carina-pty/main.zig' "$log" | grep -Fq -- '-lc'
for tool in carina-scan carina-grep carina-diff carina-patch-native carina-run carina-pty; do
  [[ -x "$work/out/$tool" ]]
done

printf 'previous\n' > "$work/out/marker"
set +e
CARINA_ZIG_BIN="$fake" \
CARINA_ZIG_TARGET="aarch64-macos.13.0" \
CARINA_ZIG_OUTPUT_DIR="$work/out" \
CARINA_ZIG_ALLOW_CUSTOM_OUTPUT=1 \
CARINA_ZIG_TEST_LOG="$log" \
CARINA_ZIG_TEST_FAIL_SWAP=1 \
  "$ROOT/scripts/build-zig-tools.sh" >/dev/null 2>&1
code=$?
set -e
[[ "$code" == "1" ]]
[[ "$(cat "$work/out/marker")" == "previous" ]]
[[ "$(find "$work/out" -maxdepth 1 -type f | wc -l | tr -d ' ')" == "7" ]]

set +e
CARINA_ZIG_BIN="$fake" \
CARINA_ZIG_TARGET="aarch64-macos.13.0" \
CARINA_ZIG_OUTPUT_DIR="$work/out" \
CARINA_ZIG_ALLOW_CUSTOM_OUTPUT=1 \
CARINA_ZIG_TEST_LOG="$log" \
CARINA_ZIG_TEST_INTERRUPT_SWAP=1 \
  "$ROOT/scripts/build-zig-tools.sh" >/dev/null 2>&1
code=$?
set -e
[[ "$code" == "143" ]]
[[ "$(cat "$work/out/marker")" == "previous" ]]

mv "$work/out" "$work/.out.previous.99999"
CARINA_ZIG_BIN="$fake" \
CARINA_ZIG_TARGET="aarch64-macos.13.0" \
CARINA_ZIG_OUTPUT_DIR="$work/out" \
CARINA_ZIG_ALLOW_CUSTOM_OUTPUT=1 \
CARINA_ZIG_TEST_LOG="$log" \
  "$ROOT/scripts/build-zig-tools.sh" >/dev/null
[[ ! -e "$work/.out.previous.99999" ]]
[[ "$(find "$work/out" -maxdepth 1 -type f | wc -l | tr -d ' ')" == "6" ]]

if CARINA_ZIG_BIN="$fake" CARINA_ZIG_TARGET="aarch64-macos.13.0" \
  CARINA_ZIG_OUTPUT_DIR="$work/unsafe" CARINA_ZIG_TEST_LOG="$log" \
  "$ROOT/scripts/build-zig-tools.sh" >/dev/null 2>&1; then
  echo "test-build-zig-tools: custom output did not require explicit opt-in" >&2
  exit 1
fi

echo "test-build-zig-tools: ok"
