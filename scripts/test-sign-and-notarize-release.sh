#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT/scripts/sign-and-notarize-release.sh"
WORKFLOW="$ROOT/.github/workflows/release.yml"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-sign-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bash -n "$SCRIPT"
: > "$work/carina_0.0.0_darwin_arm64.tar.gz"

required=(
  ARCHIVE
  APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64
  APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD
  APPLE_DEVELOPER_ID_APPLICATION_IDENTITY
  APPLE_NOTARY_APPLE_ID
  APPLE_NOTARY_TEAM_ID
  APPLE_NOTARY_PASSWORD
)

base_env=(
  "CHECK_ONLY=1"
  "ARCHIVE=$work/carina_0.0.0_darwin_arm64.tar.gz"
  "APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64=ZHVtbXk="
  "APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD=dummy"
  "APPLE_DEVELOPER_ID_APPLICATION_IDENTITY=Developer ID Application: Example Corp (ABCDE12345)"
  "APPLE_NOTARY_APPLE_ID=release@example.com"
  "APPLE_NOTARY_TEAM_ID=ABCDE12345"
  "APPLE_NOTARY_PASSWORD=dummy"
)

for missing in "${required[@]}"; do
  test_env=()
  for assignment in "${base_env[@]}"; do
    [[ "${assignment%%=*}" == "$missing" ]] || test_env+=("$assignment")
  done
  if output="$(env "${test_env[@]}" "$SCRIPT" 2>&1)"; then
    printf 'test-sign-and-notarize-release: expected missing %s to fail\n' "$missing" >&2
    exit 1
  fi
  grep -Fq "required environment variable $missing is missing" <<< "$output"
done

grep -Fq './scripts/sign-and-notarize-release.sh' "$WORKFLOW"
for required_secret in "${required[@]:1}"; do
  grep -Fq "secrets.$required_secret" "$WORKFLOW"
done

env "${base_env[@]}" "$SCRIPT" | grep -Fq 'required inputs are present'

if output="$(env "${base_env[@]}" APPLE_DEVELOPER_ID_APPLICATION_IDENTITY='Apple Development: Wrong Identity' "$SCRIPT" 2>&1)"; then
  printf 'test-sign-and-notarize-release: expected non-Developer-ID identity to fail\n' >&2
  exit 1
fi
grep -Fq 'must be a Developer ID Application identity' <<< "$output"

printf 'test-sign-and-notarize-release: ok\n'
