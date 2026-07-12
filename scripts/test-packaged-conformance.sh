#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
archive="${ARCHIVE:?ARCHIVE is required}"
[[ -f "$archive" ]] || { echo "packaged-conformance: archive not found: $archive" >&2; exit 1; }
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-conformance.XXXXXX")"
daemon_pid=""
cleanup() {
  if [[ -n "$daemon_pid" ]]; then kill "$daemon_pid" 2>/dev/null || true; wait "$daemon_pid" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT
tar -xzf "$archive" -C "$work"
mapfile_cmd=(find "$work" -mindepth 1 -maxdepth 1 -type d -print)
stages=()
while IFS= read -r path; do stages+=("$path"); done < <("${mapfile_cmd[@]}")
[[ ${#stages[@]} -eq 1 ]] || { echo "packaged-conformance: archive must contain exactly one top-level directory" >&2; exit 1; }
stage="${stages[0]}"
for binary in carina carina-daemon carina-worker carina-tui carina-kernel-service carina-scan carina-grep carina-diff carina-run carina-pty carina-patch-native headroom; do
  [[ -x "$stage/bin/$binary" ]] || { echo "packaged-conformance: missing executable $binary" >&2; exit 1; }
done
[[ -f "$stage/MANIFEST.json" && -f "$stage/checksums.txt" ]] || { echo "packaged-conformance: release metadata missing" >&2; exit 1; }
(cd "$stage" && while read -r digest path; do
  [[ -f "$path" ]] || { echo "packaged-conformance: manifest file missing: $path" >&2; exit 1; }
  if command -v sha256sum >/dev/null 2>&1; then actual="$(sha256sum "$path" | awk '{print $1}')"; else actual="$(shasum -a 256 "$path" | awk '{print $1}')"; fi
  [[ "$actual" == "$digest" ]] || { echo "packaged-conformance: checksum mismatch: $path" >&2; exit 1; }
done < checksums.txt)
"$stage/bin/carina" --version >/dev/null
socket="$work/runtime/carina.sock"
mkdir -p "$(dirname "$socket")" "$work/state"
"$stage/bin/carina-daemon" -socket "$socket" -state "$work/state" -kernel "$stage/bin/carina-kernel-service" -tools "$stage/bin" -offline -safe-mode -context-engine=off >"$work/daemon.log" 2>&1 &
daemon_pid=$!
for _ in {1..100}; do
  [[ -S "$socket" ]] && break
  kill -0 "$daemon_pid" 2>/dev/null || { cat "$work/daemon.log" >&2; exit 1; }
  sleep 0.1
done
[[ -S "$socket" ]] || { cat "$work/daemon.log" >&2; echo "packaged-conformance: daemon readiness timed out" >&2; exit 1; }
export CARINA_CONFORMANCE_SOCKET="$socket"
(cd "$root" && go test ./sdk/go -run TestRealDaemonConformance -count=1)
(cd "$root/sdk/python" && PYTHONPATH=src python3 -m unittest tests.test_conformance -v)
(cd "$root/sdk/typescript" && npm ci --ignore-scripts && npm test)
echo "packaged-conformance: ok"
