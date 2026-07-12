#!/usr/bin/env bash
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
mode="full"
external=1
allow_dirty=0
strict=0
online=0
for arg in "$@"; do
  case "$arg" in
    --check-only) mode="check" ;;
    --no-external) external=0 ;;
    --allow-dirty) allow_dirty=1 ;;
    --strict) strict=1 ;;
    --online) online=1 ;;
    *) echo "release-preflight: unknown argument $arg" >&2; exit 64 ;;
  esac
done

dist="${CARINA_PREFLIGHT_DIST:-$ROOT/dist}"
report="${CARINA_PREFLIGHT_REPORT:-$dist/release-preflight.json}"
logs="${CARINA_PREFLIGHT_LOG_DIR:-$dist/release-preflight-logs}"
infra_fail() { echo "release-preflight: infrastructure error: $*" >&2; exit 1; }
rows=""
cleanup_rows() { rm -f "$rows"; }
rows="$(mktemp "${TMPDIR:-/tmp}/carina-preflight.XXXXXX")" || infra_fail "cannot create temporary report rows"
trap cleanup_rows EXIT
mkdir -p "$(dirname "$report")" "$logs" || infra_fail "cannot create report/log directories"
: > "$rows" || infra_fail "cannot initialize temporary report rows"
failures=0
blockers=0

record() {
  local id="$1" status="$2" class="$3" detail="$4" log="${5:-}"
  detail="${detail//$'\t'/ }"; detail="${detail//$'\n'/ }"
  printf '%s\t%s\t%s\t%s\t%s\n' "$id" "$status" "$class" "$detail" "$log" >> "$rows" || infra_fail "cannot append report row"
  printf '%-7s %-30s %s\n' "$status" "$id" "$detail" || infra_fail "cannot write preflight output"
  [[ "$status" == "FAIL" ]] && failures=$((failures + 1))
  [[ "$status" == "BLOCKED" ]] && blockers=$((blockers + 1))
}

run_gate() {
  local id="$1" class="$2" detail="$3"; shift 3
  local log="$logs/$id.log" started elapsed
  : > "$log" || infra_fail "cannot initialize gate log $log"
  started="$(date +%s)" || infra_fail "cannot read clock"
  if [[ "${CARINA_PREFLIGHT_TESTING:-0}" == "1" && "${CARINA_PREFLIGHT_FAIL_GATE:-}" == "$id" ]]; then
    printf 'fault injected for %s\n' "$id" >> "$log" || infra_fail "cannot write gate log $log"
    record "$id" FAIL "$class" "fault injection proved fail-closed behavior" "$log"
    return 1
  fi
  if "$@" >> "$log" 2>&1; then
    elapsed=$(( $(date +%s) - started ))
    record "$id" PASS "$class" "$detail (${elapsed}s)" "$log"
    return 0
  fi
  elapsed=$(( $(date +%s) - started ))
  record "$id" FAIL "$class" "$detail failed after ${elapsed}s" "$log"
  tail -n 20 "$log" >&2 || true
  return 1
}

check_tools() {
  for tool in go cargo rustc node npm python3 curl tar git; do command -v "$tool" >/dev/null || { echo "missing $tool"; return 1; }; done
  python3 - "$(go env GOVERSION)" "$(node --version)" "$(rustc --version | awk '{print $2}')" <<'PY'
import re, sys
def parts(value): return tuple(map(int, re.findall(r"\d+", value)[:2]))
required = [(sys.argv[1], (1, 25), "Go"), (sys.argv[2], (24, 0), "Node"), (sys.argv[3], (1, 85), "Rust")]
for value, minimum, name in required:
    if parts(value) < minimum: raise SystemExit(f"{name} {value} is below {minimum[0]}.{minimum[1]}")
PY
  [[ "$(./scripts/zig-tool.sh version)" == "0.15.1" ]]
}

lint_workflows() {
  if command -v actionlint >/dev/null 2>&1; then
    [[ "$(actionlint -version | head -n 1)" == "v1.7.12" ]] || {
      echo "actionlint v1.7.12 is required" >&2
      return 1
    }
    actionlint .github/workflows/ci.yml .github/workflows/release.yml
    return
  fi
  if [[ "$online" == "1" ]]; then
    go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/ci.yml .github/workflows/release.yml
    return
  fi
  echo "actionlint is required for offline preflight; install v1.7.12 or use --online" >&2
  return 1
}

run_gate toolchain build "required toolchains are pinned and available" check_tools
run_gate version_matrix release "product/package/workflow versions agree" ./scripts/test-version-matrix.sh
run_gate workflow_lint release "GitHub Actions syntax and DAG are valid" lint_workflows
run_gate release_asset_contract package "exact four-archive and signing-result contract rejects omissions" ./scripts/test-verify-release-assets.sh
run_gate linux_package_contract package "deb/rpm package checksums reject omissions and corruption" ./scripts/test-verify-linux-packages.sh
run_gate windows_worker_package_contract package "Windows worker package checksums and contents reject corruption" ./scripts/test-verify-windows-worker-packages.sh
run_gate integration_package_contract package "VSIX and web operator packages reject omissions and corruption" ./scripts/test-verify-integration-packages.sh
run_gate signing_dry_run signing "signing automation rejects missing/invalid credentials" ./scripts/test-sign-and-notarize-release.sh
run_gate homebrew_formula package "Homebrew Formula renders without placeholders" ./scripts/test-homebrew-formula.sh
run_gate npm_package_contract package "five complete npm tarballs freeze reproducibly and pass offline global install" ./scripts/test-package-npm-release.sh
run_gate zig_build_contract build "pinned Zig direct-build path validates all six tools before replacing outputs" ./scripts/test-build-zig-tools.sh

if [[ "$allow_dirty" == "1" ]]; then
  record source_clean SKIP source "--allow-dirty explicitly disabled release cleanliness enforcement"
elif [[ -z "$(git status --porcelain)" ]]; then
  record source_clean PASS source "working tree is clean"
else
  record source_clean BLOCKED source "working tree has uncommitted or untracked files"
fi
if [[ "$(git branch --show-current)" == "main" ]]; then
  record source_branch PASS source "release source is main"
elif [[ "${GITHUB_ACTIONS:-}" == "true" && "${GITHUB_REF_TYPE:-}" == "tag" && "${GITHUB_SHA:-}" == "$(git rev-parse HEAD)" ]]; then
  record source_branch PASS source "tag workflow is evaluating the checked-out release commit"
else
  record source_branch BLOCKED source "release source is not main"
fi
if [[ "$online" == "1" ]]; then
  run_gate source_fetch source "origin/main refreshed through a read-only fetch" git fetch origin main
else
  record source_fetch SKIP source "offline preflight uses the locally known origin/main"
fi
if git rev-parse --verify origin/main >/dev/null 2>&1 && [[ "$(git rev-parse HEAD)" == "$(git rev-parse origin/main)" ]]; then
  record source_sync PASS source "HEAD exactly matches the locally known origin/main"
else
  record source_sync BLOCKED source "HEAD does not match origin/main; fetch and push main before tagging"
fi

package_ok=0
if [[ "$mode" == "full" ]]; then
  run_gate native_build build "Go/Rust/Zig native build" make all
  run_gate rust_kernel build "release kernel service build" cargo build --release -p carina-kernel --bin carina-kernel-service
  run_gate go_vet test "Go static analysis" go vet ./...
  run_gate rust_tests test "Rust workspace tests" cargo test --workspace
  run_gate go_race test "Go runtime race suite" bash -c 'CARINA_KERNEL_BIN="$PWD/target/release/carina-kernel-service" go test -race ./go/...'
  run_gate go_apps test "Go application tests" bash -c 'CARINA_KERNEL_BIN="$PWD/target/release/carina-kernel-service" go test ./apps/...'
  run_gate sdk_go sdk "Go SDK conformance" go test -race ./sdk/go
  run_gate sdk_typescript sdk "TypeScript SDK conformance" bash -c 'cd sdk/typescript && npm ci && npm test'
  run_gate sdk_python sdk "Python SDK conformance" bash -c 'cd sdk/python && PYTHONPATH=src python3 -m unittest discover -s tests -v'
  run_gate vscode dx "VS Code extension tests" bash -c 'cd integrations/vscode && npm ci && npm test'
  run_gate web_npm dx "web and npm launcher smoke tests" ./scripts/test-platform-packaging.sh
  run_gate acceptance test "native runtime acceptance gates" bash -c 'CARINA_KERNEL_BIN="$PWD/target/release/carina-kernel-service" ./scripts/ci-gates.sh'
  run_gate benchmark test "benchmark regression gate" ./scripts/bench.sh 10000
  version="$(go run ./scripts/product-version.go 2>/dev/null || true)"
  if run_gate archive package "current-platform archive and manifest" env VERSION="$version" SKIP_BUILD=1 SKIP_HEADROOM=1 ./scripts/package-release.sh; then
    package_ok=1
    archive="$dist/carina_${version}_$(go env GOOS)_$(go env GOARCH).tar.gz"
    run_gate archive_conformance package "packaged daemon passes all three SDK contracts" env ARCHIVE="$archive" ./scripts/test-packaged-conformance.sh
    if [[ "$(uname -s)" == "Darwin" && -x "$(command -v brew 2>/dev/null || true)" ]]; then
      if brew ruby -e 'require "os/mac"; require "os/mac/xcode"; exit(MacOS::CLT.below_minimum_version? ? 1 : 0)' >/dev/null 2>&1; then
        run_gate homebrew_install package "temporary-tap install, test, and upgrade" env VERSION="$version" GOARCH="$(go env GOARCH)" ARCHIVE="$archive" ./scripts/test-homebrew-install.sh
      else
        record homebrew_install BLOCKED package "local Command Line Tools are below Homebrew's minimum; release CI still runs the install/upgrade gate"
      fi
    else
      record homebrew_install SKIP package "requires a macOS Homebrew host; release CI runs both architectures"
    fi
  else
    record archive_conformance SKIP package "archive build failed"
    record homebrew_install SKIP package "archive build failed"
  fi
  record headroom_bundle SKIP package "release policy explicitly uses SKIP_HEADROOM=1 until reproducible cross-platform bundles exist"
else
  record full_ci_suite SKIP test "--check-only requested contract validation without long build/test gates"
fi

check_secret_names() {
  local names="$1"; shift
  local missing=()
  for name in "$@"; do grep -Fxq "$name" <<< "$names" || missing+=("$name"); done
  (( ${#missing[@]} == 0 )) || { printf 'missing secret names: %s\n' "${missing[*]}"; return 1; }
}

if [[ "$external" == "0" ]]; then
  record apple_credentials SKIP external "--no-external requested technical CI-equivalent gates only"
  record apple_notarization_evidence SKIP external "--no-external requested"
  record npm_bootstrap SKIP external "--no-external requested"
  record npm_trusted_publisher SKIP external "--no-external requested"
  record homebrew_deploy_key SKIP external "--no-external requested"
else
  repo="${GITHUB_REPOSITORY:-Nebutra/carina}"
  repo_secret_names=""
  environment_secret_names=""
  codesigning_environment=0
  codesigning_protected=0
  if [[ "$online" == "1" && "${CARINA_PREFLIGHT_EXTERNAL_OFFLINE:-0}" != "1" ]] && command -v gh >/dev/null 2>&1; then
    repo_secret_names="$(gh api "repos/$repo/actions/secrets" --paginate --jq '.secrets[].name' 2>/dev/null || true)"
    if gh api "repos/$repo/environments/codesigning" >/dev/null 2>&1; then
      codesigning_environment=1
      environment_secret_names="$(gh api "repos/$repo/environments/codesigning/secrets" --paginate --jq '.secrets[].name' 2>/dev/null || true)"
      protection_count="$(gh api "repos/$repo/environments/codesigning" --jq '.protection_rules | length' 2>/dev/null || printf 0)"
      [[ "$protection_count" =~ ^[0-9]+$ && "$protection_count" -gt 0 ]] && codesigning_protected=1
    fi
  fi
  apple=(APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64 APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD APPLE_DEVELOPER_ID_APPLICATION_IDENTITY APPLE_NOTARY_APPLE_ID APPLE_NOTARY_TEAM_ID APPLE_NOTARY_PASSWORD)
  if [[ "$codesigning_environment" == "1" && "$codesigning_protected" == "1" && -n "$environment_secret_names" ]] && check_secret_names "$environment_secret_names" "${apple[@]}"; then
    record apple_credentials PASS external "protected codesigning environment contains all required secret names; values remain workflow-validated"
  else
    record apple_credentials BLOCKED external "codesigning environment lacks protection rules and/or environment-scoped Apple secrets"
  fi
  version="$(go run ./scripts/product-version.go 2>/dev/null || true)"
  evidence_count="$(find "$dist" -maxdepth 1 -type f -name "carina_${version}_darwin_*.tar.gz*" 2>/dev/null | wc -l | tr -d ' ')"
  if [[ "$evidence_count" == "0" ]]; then
    record apple_notarization_evidence SKIP external "pre-build state has no Darwin signing evidence yet"
  elif DIST="$dist" VERSION="$version" ./scripts/verify-notary-evidence.sh >/dev/null 2>&1; then
    record apple_notarization_evidence PASS external "both Darwin archives have checksum-valid Accepted notary evidence"
  else
    record apple_notarization_evidence FAIL external "partial, corrupt, rejected, or checksum-invalid Darwin signing evidence is present"
  fi
  npm_ready=1
  npm_publishers_confirmed=0
  if [[ "$online" != "1" || "${CARINA_PREFLIGHT_EXTERNAL_OFFLINE:-0}" == "1" ]] || ! command -v gh >/dev/null 2>&1 || ! gh api "repos/$repo/environments/npm-release" >/dev/null 2>&1; then npm_ready=0; fi
  if [[ "$online" == "1" ]]; then
    for package in @nebutra/carina @nebutra/carina-darwin-arm64 @nebutra/carina-darwin-x64 @nebutra/carina-linux-arm64 @nebutra/carina-linux-x64; do
      npm view "$package" name >/dev/null 2>&1 || npm_ready=0
    done
    if [[ "$(gh api "repos/$repo/actions/variables/NPM_TRUSTED_PUBLISHERS_CONFIRMED" --jq '.value' 2>/dev/null || true)" == "true" ]]; then
      npm_publishers_confirmed=1
    fi
  fi
  if [[ "$npm_ready" == "1" ]]; then
    record npm_bootstrap PASS external "npm-release environment and all five public packages are visible"
  else
    record npm_bootstrap BLOCKED external "npm-release environment and/or five-package bootstrap is incomplete"
  fi
  if [[ "$npm_ready" == "1" && "$npm_publishers_confirmed" == "1" ]]; then
    record npm_trusted_publisher PASS external "repository attests that all five npm trusted-publisher bindings were configured; the real OIDC publish remains authoritative"
  else
    record npm_trusted_publisher BLOCKED external "set repository variable NPM_TRUSTED_PUBLISHERS_CONFIRMED=true only after configuring all five trusted-publisher bindings"
  fi
  if [[ -n "$repo_secret_names" ]] && grep -Fxq HOMEBREW_TAP_DEPLOY_KEY <<< "$repo_secret_names"; then
    record homebrew_deploy_key PASS external "Homebrew tap deploy-key secret name exists"
  else
    record homebrew_deploy_key BLOCKED external "HOMEBREW_TAP_DEPLOY_KEY is absent or not readable"
  fi
fi

if ! ROWS="$rows" REPORT="$report" MODE="$mode" STRICT="$strict" ONLINE="$online" python3 <<'PY'
import json, os
from datetime import datetime, timezone
rows = []
with open(os.environ["ROWS"], encoding="utf-8") as handle:
    for line in handle:
        gate, status, category, detail, log = line.rstrip("\n").split("\t")
        rows.append({"gate": gate, "status": status, "category": category, "detail": detail, "log": log or None})
summary = {name: sum(row["status"] == name for row in rows) for name in ("PASS", "FAIL", "BLOCKED", "SKIP")}
payload = {"schema_version": 1, "generated_at": datetime.now(timezone.utc).isoformat(), "mode": os.environ["MODE"], "strict": os.environ["STRICT"] == "1", "online_checks": os.environ["ONLINE"] == "1", "summary": summary, "gates": rows}
with open(os.environ["REPORT"], "w", encoding="utf-8") as handle:
    json.dump(payload, handle, indent=2, sort_keys=True); handle.write("\n")
PY
then
  infra_fail "cannot serialize report JSON to $report"
fi
rm -f "$rows" || infra_fail "cannot remove temporary report rows"
rows=""
trap - EXIT
report_pass="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["summary"]["PASS"])' "$report")" || infra_fail "cannot read report JSON from $report"
printf 'release-preflight: report=%s PASS=%d FAIL=%d BLOCKED=%d\n' "$report" "$report_pass" "$failures" "$blockers" || infra_fail "cannot write preflight summary"
(( failures > 0 )) && exit 1
(( strict == 1 && blockers > 0 )) && exit 2
exit 0
