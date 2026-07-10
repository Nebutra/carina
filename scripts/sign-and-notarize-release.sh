#!/usr/bin/env bash
set -euo pipefail

SCRIPT_NAME="sign-and-notarize-release"

fail() {
  printf '%s: %s\n' "$SCRIPT_NAME" "$*" >&2
  exit 1
}

require_env() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    fail "required environment variable ${name} is missing"
  fi
}

for name in \
  ARCHIVE \
  APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64 \
  APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD \
  APPLE_DEVELOPER_ID_APPLICATION_IDENTITY \
  APPLE_NOTARY_APPLE_ID \
  APPLE_NOTARY_TEAM_ID \
  APPLE_NOTARY_PASSWORD; do
  require_env "$name"
done

[[ -f "$ARCHIVE" ]] || fail "archive not found: $ARCHIVE"
[[ "$ARCHIVE" == *.tar.gz ]] || fail "ARCHIVE must end in .tar.gz"
[[ "$APPLE_DEVELOPER_ID_APPLICATION_IDENTITY" == "Developer ID Application:"* ]] || \
  fail "APPLE_DEVELOPER_ID_APPLICATION_IDENTITY must be a Developer ID Application identity"
[[ "$APPLE_NOTARY_TEAM_ID" =~ ^[A-Z0-9]{10}$ ]] || \
  fail "APPLE_NOTARY_TEAM_ID must be a 10-character Apple team id"

if [[ "${CHECK_ONLY:-0}" == "1" ]]; then
  printf '%s: required inputs are present\n' "$SCRIPT_NAME"
  exit 0
fi

[[ "$(uname -s)" == "Darwin" ]] || fail "signing and notarization require macOS"
for tool in base64 codesign ditto file python3 security shasum spctl tar uuidgen xcrun; do
  command -v "$tool" >/dev/null 2>&1 || fail "missing required tool: $tool"
done

archive_dir="$(cd "$(dirname "$ARCHIVE")" && pwd)"
archive_name="$(basename "$ARCHIVE")"
archive_path="$archive_dir/$archive_name"
package="${archive_name%.tar.gz}"
notary_result="$archive_path.notary.json"
signing_report="$archive_path.signing.txt"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-release-sign.XXXXXX")"
keychain="$work/release.keychain-db"
keychain_password="$(uuidgen)-$(uuidgen)"
profile="carina-release-$(uuidgen)"

cleanup() {
  security delete-keychain "$keychain" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

printf '%s' "$APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64" | base64 -D > "$work/developer-id.p12"
security create-keychain -p "$keychain_password" "$keychain"
security set-keychain-settings -lut 3600 "$keychain"
security unlock-keychain -p "$keychain_password" "$keychain"
security import "$work/developer-id.p12" \
  -k "$keychain" \
  -P "$APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD" \
  -T /usr/bin/codesign \
  -T /usr/bin/security
security set-key-partition-list \
  -S apple-tool:,apple:,codesign: \
  -s \
  -k "$keychain_password" \
  "$keychain"

identity_listing="$(security find-identity -v -p codesigning "$keychain")"
grep -Fq "\"$APPLE_DEVELOPER_ID_APPLICATION_IDENTITY\"" <<< "$identity_listing" || \
  fail "requested signing identity was not imported into the temporary keychain"

mkdir -p "$work/extracted"
tar -xzf "$archive_path" -C "$work/extracted"
stage="$work/extracted/$package"
[[ -d "$stage/bin" ]] || fail "archive must contain $package/bin"
[[ -f "$stage/MANIFEST.json" ]] || fail "archive must contain $package/MANIFEST.json"
[[ -f "$stage/checksums.txt" ]] || fail "archive must contain $package/checksums.txt"

signed_list="$work/signed-binaries.txt"
: > "$signed_list"
while IFS= read -r -d '' candidate; do
  if file -b "$candidate" | grep -q 'Mach-O'; then
    codesign \
      --force \
      --options runtime \
      --timestamp \
      --sign "$APPLE_DEVELOPER_ID_APPLICATION_IDENTITY" \
      --keychain "$keychain" \
      "$candidate"
    codesign --verify --strict --verbose=4 "$candidate"
    signature_details="$(codesign --display --verbose=4 "$candidate" 2>&1)"
    grep -Fq "TeamIdentifier=$APPLE_NOTARY_TEAM_ID" <<< "$signature_details" || \
      fail "signed binary $(basename "$candidate") does not match APPLE_NOTARY_TEAM_ID"
    printf '%s\n' "$candidate" >> "$signed_list"
  fi
done < <(find "$stage/bin" -type f -print0)

[[ -s "$signed_list" ]] || fail "archive contained no Mach-O binaries to sign"

refresh_release_metadata() {
  local notarization_status="$1"
  local submission_id="${2:-}"
  STAGE="$stage" \
  SIGNING_IDENTITY="$APPLE_DEVELOPER_ID_APPLICATION_IDENTITY" \
  SIGNING_TEAM_ID="$APPLE_NOTARY_TEAM_ID" \
  NOTARIZATION_STATUS="$notarization_status" \
  NOTARY_SUBMISSION_ID="$submission_id" \
    python3 <<'PY'
import hashlib
import json
import os
from pathlib import Path

stage = Path(os.environ["STAGE"])
manifest_path = stage / "MANIFEST.json"
manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
files = manifest.get("files")
if not isinstance(files, list):
    raise SystemExit("MANIFEST.json files must be an array")

checksum_lines = []
for entry in files:
    relative = entry.get("path", "")
    path = (stage / relative).resolve()
    try:
        path.relative_to(stage.resolve())
    except ValueError as exc:
        raise SystemExit(f"manifest path escapes archive root: {relative}") from exc
    if not path.is_file():
        raise SystemExit(f"manifest file is missing: {relative}")
    data = path.read_bytes()
    digest = hashlib.sha256(data).hexdigest()
    entry["bytes"] = len(data)
    entry["sha256"] = digest
    checksum_lines.append(f"{digest}  {relative}\n")

manifest["signing"] = {
    "identity": os.environ["SIGNING_IDENTITY"],
    "team_id": os.environ["SIGNING_TEAM_ID"],
    "hardened_runtime": True,
    "timestamped": True,
    "notarization_status": os.environ["NOTARIZATION_STATUS"],
    "notary_submission_id": os.environ["NOTARY_SUBMISSION_ID"],
}
(stage / "checksums.txt").write_text("".join(sorted(checksum_lines)), encoding="utf-8")
manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
}

refresh_release_metadata "submitted"

submit_zip="$work/$package-notarization.zip"
ditto -c -k --keepParent "$stage" "$submit_zip"
xcrun notarytool store-credentials "$profile" \
  --apple-id "$APPLE_NOTARY_APPLE_ID" \
  --team-id "$APPLE_NOTARY_TEAM_ID" \
  --password "$APPLE_NOTARY_PASSWORD" \
  --keychain "$keychain"
xcrun notarytool submit "$submit_zip" \
  --keychain-profile "$profile" \
  --keychain "$keychain" \
  --wait \
  --timeout "${NOTARY_TIMEOUT:-30m}" \
  --output-format json > "$notary_result"

read -r notary_status submission_id < <(
  NOTARY_RESULT="$notary_result" python3 <<'PY'
import json
import os
from pathlib import Path

result = json.loads(Path(os.environ["NOTARY_RESULT"]).read_text(encoding="utf-8"))
print(result.get("status", ""), result.get("id", ""))
PY
)
[[ "$notary_status" == "Accepted" ]] || fail "Apple notarization status was ${notary_status:-missing}; see $notary_result"
[[ -n "$submission_id" ]] || fail "Apple notarization response did not contain a submission id"

assess_notarized_binary() {
  local binary="$1"
  local attempt
  codesign --verify --strict --verbose=4 "$binary"
  codesign --display --verbose=4 "$binary"
  for attempt in {1..12}; do
    if codesign --check-notarization --verbose=4 "$binary" && \
      spctl --assess --type execute --verbose=4 "$binary"; then
      return 0
    fi
    if (( attempt < 12 )); then
      printf 'notarization ticket not visible yet; retry %d/12\n' "$attempt"
      sleep 10
    fi
  done
  return 1
}

: > "$signing_report"
while IFS= read -r binary; do
  {
    printf '==> %s\n' "${binary#$stage/}"
    assess_notarized_binary "$binary"
  } >> "$signing_report" 2>&1
done < "$signed_list"

refresh_release_metadata "accepted" "$submission_id"

rm -f "$archive_path" "$archive_path.sha256"
tar -czf "$archive_path" -C "$work/extracted" "$package"
archive_sha="$(shasum -a 256 "$archive_path" | awk '{print $1}')"
printf '%s  %s\n' "$archive_sha" "$archive_name" > "$archive_path.sha256"

# Cold-verify the final bytes rather than trusting the pre-archive staging tree.
mkdir -p "$work/final-check"
tar -xzf "$archive_path" -C "$work/final-check"
final_stage="$work/final-check/$package"
(cd "$final_stage" && shasum -a 256 -c checksums.txt)
while IFS= read -r -d '' binary; do
  if file -b "$binary" | grep -q 'Mach-O'; then
    codesign --verify --strict --verbose=4 "$binary"
  fi
done < <(find "$final_stage/bin" -type f -print0)

printf '%s: signed and notarized %s\n' "$SCRIPT_NAME" "$archive_path"
printf '%s: notary result %s\n' "$SCRIPT_NAME" "$notary_result"
printf '%s: signing report %s\n' "$SCRIPT_NAME" "$signing_report"
