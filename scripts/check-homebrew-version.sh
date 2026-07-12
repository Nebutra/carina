#!/usr/bin/env bash
set -euo pipefail

current="${1:-}"
target="${2:?target version is required}"
pattern='^[0-9]+\.[0-9]+\.[0-9]+$'
[[ "$target" =~ $pattern ]] || { echo "check-homebrew-version: invalid target $target" >&2; exit 2; }
[[ -z "$current" ]] && exit 0
[[ "$current" =~ $pattern ]] || { echo "check-homebrew-version: invalid current version $current" >&2; exit 2; }

IFS=. read -r current_major current_minor current_patch <<< "$current"
IFS=. read -r target_major target_minor target_patch <<< "$target"
current_parts=($((10#$current_major)) $((10#$current_minor)) $((10#$current_patch)))
target_parts=($((10#$target_major)) $((10#$target_minor)) $((10#$target_patch)))
for index in 0 1 2; do
  if (( target_parts[index] > current_parts[index] )); then exit 0; fi
  if (( target_parts[index] < current_parts[index] )); then
    echo "check-homebrew-version: refusing downgrade from $current to $target" >&2
    exit 1
  fi
done
exit 0
