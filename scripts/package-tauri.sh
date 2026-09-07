#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

version="${VERSION:-$(go run ./scripts/product-version.go)}"
dist="${DIST:-$ROOT/dist}"
platform="${TAURI_PLATFORM:?TAURI_PLATFORM is required (darwin or linux)}"
arch="${TAURI_ARCH:?TAURI_ARCH is required (arm64 or amd64)}"

case "$platform:$arch" in
  darwin:arm64)
    rust_target="aarch64-apple-darwin"
    bundle_types="dmg"
    ;;
  darwin:amd64)
    rust_target="x86_64-apple-darwin"
    bundle_types="dmg"
    ;;
  linux:arm64)
    rust_target="aarch64-unknown-linux-gnu"
    bundle_types="appimage,deb"
    ;;
  linux:amd64)
    rust_target="x86_64-unknown-linux-gnu"
    bundle_types="appimage,deb"
    ;;
  *)
    echo "package-tauri: unsupported platform/architecture $platform:$arch" >&2
    exit 2
    ;;
esac

tauri_version="$(node -p "require('./integrations/tauri/package.json').version")"
[[ "$tauri_version" == "$version" ]] || {
  echo "package-tauri: Tauri version $tauri_version != product version $version" >&2
  exit 1
}

mkdir -p "$dist"
if [[ "${TAURI_SKIP_BUILD:-0}" == "1" ]]; then
  bundle_root="${TAURI_BUNDLE_ROOT:?TAURI_BUNDLE_ROOT is required when TAURI_SKIP_BUILD=1}"
else
  # Install both lockfile owners outside an npm lifecycle. Nested `npm ci`
  # inherits npm_config_* values from the parent lifecycle and is rejected by
  # npm 11 when user-level allowScripts is reinterpreted as project config.
  (
    cd integrations/web
    npm ci
  )
  (
    cd integrations/tauri
    npm ci
    npm run build -- --target "$rust_target" --bundles "$bundle_types"
  )
  bundle_root="$ROOT/integrations/tauri/src-tauri/target/$rust_target/release/bundle"
fi

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

stage_bundle() {
  local bundle_dir="$1"
  local suffix="$2"
  local output_name="$3"
  local matches=()
  local candidate
  while IFS= read -r -d '' candidate; do
    matches+=("$candidate")
  done < <(find "$bundle_root/$bundle_dir" -maxdepth 1 -type f -name "*${version}*${suffix}" -print0 2>/dev/null)
  [[ "${#matches[@]}" == "1" ]] || {
    echo "package-tauri: expected one $bundle_dir $suffix bundle for $version, found ${#matches[@]}" >&2
    exit 1
  }
  local output="$dist/$output_name"
  cp "${matches[0]}" "$output"
  printf '%s  %s\n' "$(sha256_file "$output")" "$(basename "$output")" > "$output.sha256"
}

case "$platform" in
  darwin)
    stage_bundle dmg .dmg "carina-harness_${version}_darwin_${arch}.dmg"
    ;;
  linux)
    stage_bundle appimage .AppImage "carina-harness_${version}_linux_${arch}.AppImage"
    stage_bundle deb .deb "carina-harness_${version}_linux_${arch}.deb"
    ;;
esac

echo "package-tauri: packaged Carina Harness $version for $platform/$arch"
