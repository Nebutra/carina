#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

extract_toml_version() {
  local file="$1"
  sed -nE 's/^version = "([^"]+)"/\1/p' "$file" | head -n 1
}

extract_json_version() {
  local file="$1"
  sed -nE 's/^  "version": "([^"]+)".*/\1/p' "$file" | head -n 1
}

json_escape() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  s="${s//$'\n'/\\n}"
  s="${s//$'\r'/\\r}"
  printf '%s' "$s"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

file_size() {
  if stat -f%z "$1" >/dev/null 2>&1; then
    stat -f%z "$1"
  else
    stat -c%s "$1"
  fi
}

need_file() {
  if [[ ! -f "$1" ]]; then
    printf 'package-release: missing required file: %s\n' "$1" >&2
    exit 1
  fi
}

copy_file() {
  local src="$1"
  local dst="$2"
  need_file "$src"
  cp "$src" "$dst"
}

lock_value() {
  local file="$1"
  local key="$2"
  sed -nE "s/^${key} = \"([^\"]*)\"/\\1/p" "$file" | head -n 1
}

lock_artifact_value() {
  local file="$1"
  local target="$2"
  local key="$3"
  awk -v section="[artifacts.${target}]" -v wanted="$key" '
    $0 == section { active = 1; next }
    /^\[/ { active = 0 }
    active && $1 == wanted {
      sub(/^[^=]+= */, "")
      gsub(/^"|"$/, "")
      print
      exit
    }
  ' "$file"
}

product_version="$(go run ./scripts/product-version.go)"
cli_version="$product_version"
daemon_version="$product_version"
cargo_version="$(extract_toml_version Cargo.toml)"
ts_sdk_version="$(extract_json_version sdk/typescript/package.json)"
py_sdk_version="$(extract_toml_version sdk/python/pyproject.toml)"

version="${VERSION:-$product_version}"
if [[ -z "$version" ]]; then
  printf 'package-release: VERSION is required when product version cannot be loaded\n' >&2
  exit 1
fi
if [[ "$version" != "$product_version" ]]; then
  printf 'package-release: VERSION %s does not match product version %s\n' "$version" "$product_version" >&2
  exit 1
fi

warnings=()

missing=()
for tool in go cargo; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    missing+=("$tool")
  fi
done
if (( ${#missing[@]} > 0 )); then
  printf 'package-release: missing required tool(s): %s\n' "${missing[*]}" >&2
  exit 127
fi
if [[ "${SKIP_BUILD:-0}" != "1" && "${SKIP_ZIG:-0}" != "1" ]] && ! "$ROOT/scripts/zig-tool.sh" version >/dev/null 2>&1; then
  printf 'package-release: missing required tool: zig\n' >&2
  printf 'Install Zig 0.15.1, enable the pinned installer, or set SKIP_ZIG=1 to package existing Zig artifacts.\n' >&2
  exit 127
fi

goos="${GOOS:-$(go env GOOS)}"
goarch="${GOARCH:-$(go env GOARCH)}"
git_sha="$(git rev-parse --short HEAD 2>/dev/null || printf unknown)"
created_at="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
package="carina_${version}_${goos}_${goarch}"
dist_dir="$ROOT/dist"
stage="$dist_dir/$package"
archive="$dist_dir/$package.tar.gz"
headroom_lock="$ROOT/integrations/headroom.lock"
headroom_target="${goos}-${goarch}"
headroom_status="not packaged"
headroom_version=""
headroom_source_url=""
headroom_source_sha256=""
headroom_bundle_sha256=""
headroom_bundle_path=""

if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
  printf '==> build Go apps\n'
  make go
  printf '==> build Rust kernel service\n'
  cargo build --release -p carina-kernel --bin carina-kernel-service
  if [[ "${SKIP_ZIG:-0}" == "1" ]]; then
    warnings+=("SKIP_ZIG=1: reused existing zig/zig-out/bin/carina-* artifacts without rebuilding Zig tools")
  else
    printf '==> build Zig native tools\n'
    "$ROOT/scripts/build-zig-tools.sh"
  fi
else
  printf '==> SKIP_BUILD=1: packaging existing local artifacts\n'
  warnings+=("SKIP_BUILD=1: packaged existing local artifacts without rebuilding")
fi

rm -rf "$stage" "$archive" "$archive.sha256"
mkdir -p "$stage/bin" "$stage/docs"

copy_file bin/carina "$stage/bin/carina"
copy_file bin/carina-daemon "$stage/bin/carina-daemon"
copy_file bin/carina-worker "$stage/bin/carina-worker"
copy_file bin/carina-tui "$stage/bin/carina-tui"
copy_file target/release/carina-kernel-service "$stage/bin/carina-kernel-service"

if [[ "${SKIP_HEADROOM:-0}" == "1" ]]; then
  warnings+=("SKIP_HEADROOM=1: packaged without optional Headroom; context_engine=auto will use the noop fallback")
  headroom_status="skipped"
else
  need_file "$headroom_lock"
  headroom_version="$(lock_value "$headroom_lock" version)"
  headroom_source_url="$(lock_artifact_value "$headroom_lock" "$headroom_target" source_url)"
  headroom_source_sha256="$(lock_artifact_value "$headroom_lock" "$headroom_target" source_sha256)"
  headroom_bundle_path="$(lock_artifact_value "$headroom_lock" "$headroom_target" bundle_path)"
  if [[ -z "$headroom_bundle_path" || -z "$headroom_source_url" || -z "$headroom_source_sha256" ]]; then
    printf 'package-release: no Headroom lock entry for target %s in %s\n' "$headroom_target" "$headroom_lock" >&2
    exit 1
  fi
  if [[ -n "${HEADROOM_BIN:-}" ]]; then
    headroom_src="$HEADROOM_BIN"
    if [[ -z "${HEADROOM_BUNDLE_SHA256:-}" ]]; then
      printf 'package-release: HEADROOM_BUNDLE_SHA256 is required with HEADROOM_BIN\n' >&2
      exit 1
    fi
    headroom_bundle_sha256="$HEADROOM_BUNDLE_SHA256"
  else
    headroom_src="$ROOT/$headroom_bundle_path"
    printf '==> build pinned Headroom bundle for %s\n' "$headroom_target"
    HEADROOM_TARGET="$headroom_target" OUTPUT="$headroom_src" "$ROOT/scripts/build-headroom-bundle.sh"
    headroom_bundle_sha256="$(sha256_file "$headroom_src")"
  fi
  need_file "$headroom_src"
  actual_headroom_sha="$(sha256_file "$headroom_src")"
  if [[ "$actual_headroom_sha" != "$headroom_bundle_sha256" ]]; then
    printf 'package-release: Headroom sha256 mismatch for %s\n' "$headroom_src" >&2
    printf '  want: %s\n' "$headroom_bundle_sha256" >&2
    printf '   got: %s\n' "$actual_headroom_sha" >&2
    exit 1
  fi
  cp "$headroom_src" "$stage/bin/headroom"
  chmod 755 "$stage/bin/headroom"
  "$stage/bin/headroom" --help >/dev/null
  headroom_status="bundled"
fi

for name in carina-scan carina-grep carina-diff carina-run carina-pty carina-patch-native; do
  copy_file "zig/zig-out/bin/$name" "$stage/bin/$name"
  chmod 755 "$stage/bin/$name"
done

copy_file README.md "$stage/README.md"
copy_file LICENSE "$stage/LICENSE"
copy_file SECURITY.md "$stage/SECURITY.md"
copy_file docs/release.md "$stage/docs/release.md"
copy_file docs/roadmap.md "$stage/docs/roadmap.md"
copy_file docs/rpc-api.md "$stage/docs/rpc-api.md"

for label_value in \
  "cli:$cli_version"; do
  label="${label_value%%:*}"
  value="${label_value#*:}"
  if [[ -n "$value" && "$value" != "$version" ]]; then
    warnings+=("$label version $value differs from package version $version")
  fi
done

{
  printf 'Package version: %s\n' "$version"
  printf 'Git SHA: %s\n' "$git_sha"
  printf 'Target: %s/%s\n\n' "$goos" "$goarch"
  printf 'Component versions:\n'
  printf -- '- cli: %s\n' "${cli_version:-unknown}"
  printf -- '- daemon: %s\n' "${daemon_version:-unknown}"
  printf -- '- cargo: %s\n' "${cargo_version:-unknown}"
  printf -- '- typescript_sdk: %s\n' "${ts_sdk_version:-unknown}"
  printf -- '- python_sdk: %s\n' "${py_sdk_version:-unknown}"
  printf -- '- headroom: %s\n' "${headroom_version:-none}"
  printf '\nHeadroom:\n'
  printf -- '- status: %s\n' "$headroom_status"
  printf -- '- target: %s\n' "$headroom_target"
  printf -- '- lock: %s\n' "${headroom_lock#$ROOT/}"
  printf -- '- source_url: %s\n' "${headroom_source_url:-}"
  printf -- '- source_sha256: %s\n' "${headroom_source_sha256:-}"
  printf -- '- bundle_path: %s\n' "${headroom_bundle_path:-}"
  printf -- '- bundle_sha256: %s\n' "${headroom_bundle_sha256:-}"
  printf '\nWarnings:\n'
  if (( ${#warnings[@]} == 0 )); then
    printf -- '- none\n'
  else
    for warning in "${warnings[@]}"; do
      printf -- '- %s\n' "$warning"
    done
  fi
} > "$stage/VERSION_CHECK.txt"

tmp_files="$dist_dir/.package-files.$$"
find "$stage" -type f | sort > "$tmp_files"
{
  while IFS= read -r file; do
    rel="${file#$stage/}"
    printf '%s  %s\n' "$(sha256_file "$file")" "$rel"
  done < "$tmp_files"
} > "$stage/checksums.txt"

manifest="$stage/MANIFEST.json"
{
  printf '{\n'
  printf '  "name": "carina",\n'
  printf '  "version": "%s",\n' "$(json_escape "$version")"
  printf '  "git_sha": "%s",\n' "$(json_escape "$git_sha")"
  printf '  "created_at": "%s",\n' "$created_at"
  printf '  "target": {"goos": "%s", "goarch": "%s"},\n' "$(json_escape "$goos")" "$(json_escape "$goarch")"
  printf '  "versions": {\n'
  printf '    "cli": "%s",\n' "$(json_escape "${cli_version:-}")"
  printf '    "daemon": "%s",\n' "$(json_escape "${daemon_version:-}")"
  printf '    "cargo": "%s",\n' "$(json_escape "${cargo_version:-}")"
  printf '    "typescript_sdk": "%s",\n' "$(json_escape "${ts_sdk_version:-}")"
  printf '    "python_sdk": "%s"\n' "$(json_escape "${py_sdk_version:-}")"
  printf '  },\n'
  printf '  "headroom": {\n'
  printf '    "status": "%s",\n' "$(json_escape "$headroom_status")"
  printf '    "version": "%s",\n' "$(json_escape "${headroom_version:-}")"
  printf '    "target": "%s",\n' "$(json_escape "$headroom_target")"
  printf '    "lock": "integrations/headroom.lock",\n'
  printf '    "source_url": "%s",\n' "$(json_escape "${headroom_source_url:-}")"
  printf '    "source_sha256": "%s",\n' "$(json_escape "${headroom_source_sha256:-}")"
  printf '    "bundle_path": "%s",\n' "$(json_escape "${headroom_bundle_path:-}")"
  printf '    "bundle_sha256": "%s"\n' "$(json_escape "${headroom_bundle_sha256:-}")"
  printf '  },\n'
  printf '  "warnings": [\n'
  for i in "${!warnings[@]}"; do
    comma=","
    if (( i == ${#warnings[@]} - 1 )); then
      comma=""
    fi
    printf '    "%s"%s\n' "$(json_escape "${warnings[$i]}")" "$comma"
  done
  printf '  ],\n'
  printf '  "files": [\n'
  first=1
  while IFS= read -r file; do
    rel="${file#$stage/}"
    if (( first == 0 )); then
      printf ',\n'
    fi
    first=0
    printf '    {"path": "%s", "bytes": %s, "sha256": "%s"}' \
      "$(json_escape "$rel")" "$(file_size "$file")" "$(sha256_file "$file")"
  done < "$tmp_files"
  printf '\n  ]\n'
  printf '}\n'
} > "$manifest"
rm -f "$tmp_files"

"$stage/bin/carina" --version >/dev/null

COPYFILE_DISABLE=1 tar -czf "$archive" -C "$dist_dir" "$package"
archive_sha="$(sha256_file "$archive")"
printf '%s  %s\n' "$archive_sha" "$(basename "$archive")" > "$archive.sha256"
printf '%s  %s\n' "$archive_sha" "$(basename "$archive")" > "$dist_dir/SHA256SUMS"

printf 'release package: %s\n' "$archive"
printf 'checksum: %s\n' "$archive.sha256"
printf 'manifest: %s\n' "$manifest"
