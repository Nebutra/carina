#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -n "${CARINA_ZIG_OUTPUT_DIR:-}" && "${CARINA_ZIG_ALLOW_CUSTOM_OUTPUT:-0}" != "1" ]]; then
  echo "build-zig-tools: CARINA_ZIG_OUTPUT_DIR requires CARINA_ZIG_ALLOW_CUSTOM_OUTPUT=1" >&2
  exit 64
fi
output="${CARINA_ZIG_OUTPUT_DIR:-$ROOT/zig/zig-out/bin}"
target="${CARINA_ZIG_TARGET:-}"

if [[ -z "$target" ]]; then
  case "$(uname -s)-$(uname -m)" in
    Darwin-arm64) target="aarch64-macos.13.0" ;;
    Darwin-x86_64) target="x86_64-macos.13.0" ;;
  esac
fi

mkdir -p "$(dirname "$output")"
parent="$(dirname "$output")"
base="$(basename "$output")"
stale=("$parent/.${base}.previous."*)
if [[ -e "${stale[0]}" ]]; then
  if [[ -e "$output" ]]; then
    rm -rf "${stale[@]}"
  else
    [[ "${#stale[@]}" == "1" ]] || { echo "build-zig-tools: multiple interrupted backups require manual recovery" >&2; exit 1; }
    mv "${stale[0]}" "$output"
  fi
fi
stage="$(mktemp -d "$(dirname "$output")/.carina-zig-tools.XXXXXX")"
backup="$parent/.${base}.previous.$$"
had_output=0
cleanup() {
  status=$?
  if [[ "$had_output" == "1" && ! -e "$output" && -e "$backup" ]]; then
    mv "$backup" "$output" || true
  fi
  [[ -n "$stage" ]] && rm -rf "$stage"
  [[ -e "$output" ]] && rm -rf "$backup"
  return "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

tools=(
  carina-scan
  carina-grep
  carina-diff
  carina-patch-native
  carina-run
  carina-pty
)

for tool in "${tools[@]}"; do
  args=(build-exe --cache-dir "$ROOT/zig/.zig-cache/direct")
  if [[ -n "$target" ]]; then
    args+=(-target "$target")
  fi
  args+=(--dep jsonl "-Mroot=$ROOT/zig/$tool/main.zig")
  if [[ -n "$target" ]]; then
    args+=(-target "$target")
  fi
  args+=("-Mjsonl=$ROOT/zig/common/jsonl.zig" --name "$tool" "-femit-bin=$stage/$tool")
  if [[ "$tool" == "carina-pty" ]]; then
    args+=(-lc)
  fi
  "$ROOT/scripts/zig-tool.sh" "${args[@]}"
done

for tool in "${tools[@]}"; do
  [[ -x "$stage/$tool" ]] || { echo "build-zig-tools: staged tool is missing: $tool" >&2; exit 1; }
done
[[ "$(find "$stage" -maxdepth 1 -type f | wc -l | tr -d ' ')" == "6" ]] || {
  echo "build-zig-tools: staged output contains unexpected files" >&2
  exit 1
}

if [[ -e "$output" ]]; then
  # Enter recovery state before the rename. A signal delivered immediately
  # after mv must still make cleanup restore the previous output.
  had_output=1
  if ! mv "$output" "$backup"; then
    had_output=0
    echo "build-zig-tools: cannot preserve previous output" >&2
    exit 1
  fi
fi
if [[ "${CARINA_ZIG_TEST_INTERRUPT_SWAP:-0}" == "1" ]]; then
  kill -TERM "$$"
fi
if [[ "${CARINA_ZIG_TEST_FAIL_SWAP:-0}" == "1" ]] || ! mv "$stage" "$output"; then
  [[ "$had_output" == "1" ]] && mv "$backup" "$output"
  echo "build-zig-tools: output swap failed; previous tools restored" >&2
  exit 1
fi
stage=""
rm -rf "$backup"

printf 'build-zig-tools: built %d tools%s\n' "${#tools[@]}" "${target:+ for $target}"
