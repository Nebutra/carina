#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT/scripts/sign-and-notarize-release.sh"
WORKFLOW="$ROOT/.github/workflows/release.yml"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-sign-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bash -n "$SCRIPT"
: > "$work/carina_0.0.0_darwin_arm64.tar.gz"

common_required=(
  ARCHIVE
  APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64
  APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD
  APPLE_DEVELOPER_ID_APPLICATION_IDENTITY
  APPLE_NOTARY_TEAM_ID
)

common_env=(
  "CHECK_ONLY=1"
  "ARCHIVE=$work/carina_0.0.0_darwin_arm64.tar.gz"
  "APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64=ZHVtbXk="
  "APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD=dummy"
  "APPLE_DEVELOPER_ID_APPLICATION_IDENTITY=Developer ID Application: Example Corp (ABCDE12345)"
  "APPLE_NOTARY_TEAM_ID=ABCDE12345"
)
apple_id_env=(
  "${common_env[@]}"
  "APPLE_NOTARY_APPLE_ID=release@example.com"
  "APPLE_NOTARY_PASSWORD=dummy"
)
api_key_env=(
  "${common_env[@]}"
  $'APPLE_NOTARY_KEY_P8=-----BEGIN PRIVATE KEY-----\ndummy\n-----END PRIVATE KEY-----'
  "APPLE_NOTARY_KEY_ID=ABCDEFGHIJ"
  "APPLE_NOTARY_ISSUER_ID=01234567-89ab-cdef-0123-456789abcdef"
)

for missing in "${common_required[@]}"; do
  test_env=()
  for assignment in "${apple_id_env[@]}"; do
    [[ "${assignment%%=*}" == "$missing" ]] || test_env+=("$assignment")
  done
  if output="$(env "${test_env[@]}" "$SCRIPT" 2>&1)"; then
    printf 'test-sign-and-notarize-release: expected missing %s to fail\n' "$missing" >&2
    exit 1
  fi
  grep -Fq "required environment variable $missing is missing" <<< "$output"
done

for missing in APPLE_NOTARY_APPLE_ID APPLE_NOTARY_PASSWORD; do
  test_env=()
  for assignment in "${apple_id_env[@]}"; do
    [[ "${assignment%%=*}" == "$missing" ]] || test_env+=("$assignment")
  done
  if output="$(env "${test_env[@]}" "$SCRIPT" 2>&1)"; then
    printf 'test-sign-and-notarize-release: expected missing %s to fail\n' "$missing" >&2
    exit 1
  fi
  grep -Fq "required environment variable $missing is missing" <<< "$output"
done

for missing in APPLE_NOTARY_KEY_P8 APPLE_NOTARY_KEY_ID APPLE_NOTARY_ISSUER_ID; do
  test_env=()
  for assignment in "${api_key_env[@]}"; do
    [[ "${assignment%%=*}" == "$missing" ]] || test_env+=("$assignment")
  done
  if output="$(env "${test_env[@]}" "$SCRIPT" 2>&1)"; then
    printf 'test-sign-and-notarize-release: expected missing %s to fail\n' "$missing" >&2
    exit 1
  fi
  grep -Fq "required environment variable $missing is missing" <<< "$output"
done

grep -Fq './scripts/sign-and-notarize-release.sh' "$WORKFLOW"
for required_secret in "${common_required[@]:1}" APPLE_NOTARY_APPLE_ID APPLE_NOTARY_PASSWORD APPLE_NOTARY_KEY_P8 APPLE_NOTARY_KEY_ID APPLE_NOTARY_ISSUER_ID; do
  grep -Fq "secrets.$required_secret" "$WORKFLOW"
done

env "${apple_id_env[@]}" "$SCRIPT" | grep -Fq 'required inputs are present'
env "${api_key_env[@]}" "$SCRIPT" | grep -Fq 'required inputs are present'

if output="$(env "${api_key_env[@]}" APPLE_DEVELOPER_ID_APPLICATION_IDENTITY='Apple Development: Wrong Identity' "$SCRIPT" 2>&1)"; then
  printf 'test-sign-and-notarize-release: expected non-Developer-ID identity to fail\n' >&2
  exit 1
fi
grep -Fq 'must be a Developer ID Application identity' <<< "$output"

if output="$(env "${api_key_env[@]}" APPLE_NOTARY_KEY_ID='short' "$SCRIPT" 2>&1)"; then
  printf 'test-sign-and-notarize-release: expected invalid API key id to fail\n' >&2
  exit 1
fi
grep -Fq 'must be a 10-character App Store Connect key id' <<< "$output"

printf 'test-sign-and-notarize-release: ok\n'
