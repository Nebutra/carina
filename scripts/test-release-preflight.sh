#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-preflight-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
cat > "$work/bin/actionlint" <<'SH'
#!/bin/sh
if [ "${1:-}" = "-version" ]; then echo v1.7.12; fi
exit 0
SH
chmod +x "$work/bin/actionlint"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}
dump_failures() {
  local report="$1"
  [[ -f "$report" ]] || return 0
  python3 - "$report" <<'PY' >&2
import json, sys
data = json.load(open(sys.argv[1]))
for gate in data.get("gates", []):
    if gate.get("status") == "FAIL":
        print(f'{gate["gate"]}: {gate["detail"]} log={gate.get("log")}')
        if gate.get("log"):
            try:
                print(open(gate["log"], encoding="utf-8").read())
            except OSError as error:
                print(f"cannot read gate log: {error}")
PY
}

report="$work/failure.json"
set +e
CARINA_PREFLIGHT_TESTING=1 CARINA_PREFLIGHT_FAIL_GATE=version_matrix CARINA_PREFLIGHT_REPORT="$report" \
  CARINA_PREFLIGHT_DIST="$work/dist-failure" env PATH="$work/bin:$PATH" "$ROOT/scripts/release-preflight.sh" --check-only --no-external --allow-dirty >/dev/null 2>&1
code=$?
set -e
[[ "$code" == "1" ]] || { echo "test-release-preflight: technical failure exit=$code want=1" >&2; exit 1; }
python3 - "$report" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
assert any(g["gate"] == "version_matrix" and g["status"] == "FAIL" for g in data["gates"])
PY

report="$work/developer-blocked.json"
set +e
CARINA_PREFLIGHT_TESTING=1 CARINA_PREFLIGHT_EXTERNAL_OFFLINE=1 CARINA_PREFLIGHT_REPORT="$report" \
  CARINA_PREFLIGHT_DIST="$work/dist-developer" env PATH="$work/bin:$PATH" "$ROOT/scripts/release-preflight.sh" --check-only --allow-dirty >/dev/null 2>&1
code=$?
set -e
[[ "$code" == "0" ]] || { dump_failures "$report"; echo "test-release-preflight: developer blocker exit=$code want=0" >&2; exit 1; }
python3 - "$report" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
assert data["summary"]["FAIL"] == 0
assert data["summary"]["BLOCKED"] >= 3
PY

report="$work/strict-blocked.json"
set +e
CARINA_PREFLIGHT_TESTING=1 CARINA_PREFLIGHT_EXTERNAL_OFFLINE=1 CARINA_PREFLIGHT_REPORT="$report" \
  CARINA_PREFLIGHT_DIST="$work/dist-strict" env PATH="$work/bin:$PATH" "$ROOT/scripts/release-preflight.sh" --check-only --allow-dirty --strict >/dev/null 2>&1
code=$?
set -e
[[ "$code" == "2" ]] || { echo "test-release-preflight: strict blocker exit=$code want=2" >&2; exit 1; }

set +e
CARINA_PREFLIGHT_REPORT="/dev/null/report.json" CARINA_PREFLIGHT_DIST="$work/dist-unwritable" \
  env PATH="$work/bin:$PATH" "$ROOT/scripts/release-preflight.sh" --check-only --no-external --allow-dirty >/dev/null 2>&1
code=$?
set -e
[[ "$code" == "1" ]] || { echo "test-release-preflight: unwritable report exit=$code want=1" >&2; exit 1; }

version="$(cd "$ROOT" && go run ./scripts/product-version.go)"
partial="$work/dist-partial"
mkdir -p "$partial"
printf 'partial\n' > "$partial/carina_${version}_darwin_arm64.tar.gz"
report="$work/partial.json"
set +e
CARINA_PREFLIGHT_EXTERNAL_OFFLINE=1 CARINA_PREFLIGHT_REPORT="$report" CARINA_PREFLIGHT_DIST="$partial" \
  env PATH="$work/bin:$PATH" "$ROOT/scripts/release-preflight.sh" --check-only --allow-dirty >/dev/null 2>&1
code=$?
set -e
[[ "$code" == "1" ]] || { echo "test-release-preflight: partial notary evidence exit=$code want=1" >&2; exit 1; }
python3 - "$report" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
assert any(g["gate"] == "apple_notarization_evidence" and g["status"] == "FAIL" for g in data["gates"])
PY

complete="$work/dist-complete"
mkdir -p "$complete"
for arch in arm64 amd64; do
  archive="$complete/carina_${version}_darwin_${arch}.tar.gz"
  printf '%s\n' "$arch" > "$archive"
  digest="$(sha256_file "$archive")"
  printf '%s  %s\n' "$digest" "$(basename "$archive")" > "$archive.sha256"
  printf '{"status":"Accepted","id":"submission-%s"}\n' "$arch" > "$archive.notary.json"
  printf 'verified %s\n' "$arch" > "$archive.signing.txt"
done
report="$work/complete.json"
CARINA_PREFLIGHT_EXTERNAL_OFFLINE=1 CARINA_PREFLIGHT_REPORT="$report" CARINA_PREFLIGHT_DIST="$complete" \
  env PATH="$work/bin:$PATH" "$ROOT/scripts/release-preflight.sh" --check-only --allow-dirty >/dev/null 2>&1
python3 - "$report" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
assert any(g["gate"] == "apple_notarization_evidence" and g["status"] == "PASS" for g in data["gates"])
PY

real_npm="$(command -v npm)"
cat > "$work/bin/npm" <<'SH'
#!/bin/sh
if [ "${1:-}" = "view" ]; then echo "${2:-package}"; exit 0; fi
exec "$REAL_NPM" "$@"
SH
chmod +x "$work/bin/npm"
cat > "$work/bin/gh" <<'SH'
#!/bin/sh
args="$*"
case "$args" in
  *environments/codesigning/secrets*) exit 0 ;;
  *environments/codesigning*--jq*) echo 1; exit 0 ;;
  *environments/codesigning*) echo '{}'; exit 0 ;;
  *environments/npm-release*) echo '{}'; exit 0 ;;
  *actions/variables/NPM_TRUSTED_PUBLISHERS_CONFIRMED*) echo true; exit 0 ;;
  *actions/secrets*)
    printf '%s\n' \
      APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64 \
      APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD \
      APPLE_DEVELOPER_ID_APPLICATION_IDENTITY \
      APPLE_NOTARY_APPLE_ID APPLE_NOTARY_TEAM_ID APPLE_NOTARY_PASSWORD \
      HOMEBREW_TAP_DEPLOY_KEY
    exit 0
    ;;
esac
exit 1
SH
chmod +x "$work/bin/gh"
report="$work/environment-isolation.json"
REAL_NPM="$real_npm" CARINA_PREFLIGHT_REPORT="$report" CARINA_PREFLIGHT_DIST="$work/dist-isolation" \
  env PATH="$work/bin:$PATH" "$ROOT/scripts/release-preflight.sh" --check-only --allow-dirty --online >/dev/null 2>&1
python3 - "$report" <<'PY'
import json, sys
data=json.load(open(sys.argv[1]))
gates={g["gate"]: g["status"] for g in data["gates"]}
assert gates["apple_credentials"] == "BLOCKED", gates
assert gates["npm_bootstrap"] == "PASS", gates
assert gates["npm_trusted_publisher"] == "PASS", gates
PY
echo "test-release-preflight: ok"
