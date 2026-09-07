#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-tauri-package-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT
version="$(go run "$ROOT/scripts/product-version.go")"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

stage_case() {
  local platform="$1"
  local arch="$2"
  local bundle_dir="$3"
  local upstream_name="$4"
  local expected_name="$5"
  local bundle_root="$work/bundles-$platform-$arch"
  local output="$work/dist-$platform-$arch"
  mkdir -p "$bundle_root/$bundle_dir"
  printf 'fixture %s/%s\n' "$platform" "$arch" > "$bundle_root/$bundle_dir/$upstream_name"
  VERSION="$version" \
    DIST="$output" \
    TAURI_PLATFORM="$platform" \
    TAURI_ARCH="$arch" \
    TAURI_SKIP_BUILD=1 \
    TAURI_BUNDLE_ROOT="$bundle_root" \
    "$ROOT/scripts/package-tauri.sh" >/dev/null
  local artifact="$output/$expected_name"
  [[ -f "$artifact" && -f "$artifact.sha256" ]]
  read -r digest filename < "$artifact.sha256"
  [[ "$filename" == "$expected_name" ]]
  [[ "$digest" == "$(sha256_file "$artifact")" ]]
}

stage_case darwin arm64 dmg "Carina Harness_${version}_aarch64.dmg" "carina-harness_${version}_darwin_arm64.dmg"
stage_case darwin amd64 dmg "Carina Harness_${version}_x64.dmg" "carina-harness_${version}_darwin_amd64.dmg"

for arch in arm64 amd64; do
  bundle_root="$work/bundles-linux-$arch"
  output="$work/dist-linux-$arch"
  mkdir -p "$bundle_root/appimage" "$bundle_root/deb"
  printf 'appimage\n' > "$bundle_root/appimage/Carina Harness_${version}_${arch}.AppImage"
  printf 'deb\n' > "$bundle_root/deb/Carina Harness_${version}_${arch}.deb"
  VERSION="$version" DIST="$output" TAURI_PLATFORM=linux TAURI_ARCH="$arch" \
    TAURI_SKIP_BUILD=1 TAURI_BUNDLE_ROOT="$bundle_root" \
    "$ROOT/scripts/package-tauri.sh" >/dev/null
  for suffix in AppImage deb; do
    artifact="$output/carina-harness_${version}_linux_${arch}.${suffix}"
    [[ -f "$artifact" && -f "$artifact.sha256" ]]
  done
done

if VERSION="$version" DIST="$work/invalid" TAURI_PLATFORM=windows TAURI_ARCH=amd64 \
  TAURI_SKIP_BUILD=1 TAURI_BUNDLE_ROOT="$work/missing" \
  "$ROOT/scripts/package-tauri.sh" >/dev/null 2>&1; then
  echo "test-package-tauri: unsupported Windows desktop package was accepted" >&2
  exit 1
fi

echo "test-package-tauri: ok"
