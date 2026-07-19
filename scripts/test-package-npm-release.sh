#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-npm-package-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT
dist="$work/dist"
mkdir -p "$dist" "$work/bin"

cat > "$work/bin/syft" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
counter_file="${SYFT_COUNTER_FILE:?}"
counter=0
[[ -f "$counter_file" ]] && counter="$(cat "$counter_file")"
counter=$((counter + 1))
printf '%s\n' "$counter" > "$counter_file"
for arg in "$@"; do
  case "$arg" in
    spdx-json=*)
      printf '{"creationInfo":{"created":"2026-07-12T00:00:%02dZ"},"documentNamespace":"https://syft.invalid/random/%d","packages":[]}\n' "$counter" "$counter" > "${arg#spdx-json=}"
      ;;
  esac
done
SH
chmod +x "$work/bin/syft"
version="$(cd "$ROOT" && go run ./scripts/product-version.go)"
sha256_manifest() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

for tuple in darwin:arm64 linux:arm64 darwin:amd64 linux:amd64; do
  platform="${tuple%%:*}"
  arch="${tuple##*:}"
  stage="carina_${version}_${platform}_${arch}"
  mkdir -p "$work/$stage/bin"
  for binary in \
    carina carina-daemon carina-worker carina-kernel-service \
    carina-scan carina-grep carina-diff carina-patch-native carina-run carina-pty headroom; do
    printf '#!/bin/sh\nexit 0\n' > "$work/$stage/bin/$binary"
    chmod +x "$work/$stage/bin/$binary"
  done
  tar -czf "$dist/$stage.tar.gz" -C "$work" "$stage"
done

counter="$work/syft-counter"
(cd "$ROOT" && PATH="$work/bin:$PATH" SYFT_COUNTER_FILE="$counter" DIST_DIR="$dist" VERSION="$version" ./scripts/package-npm-release.sh)
[[ "$(find "$dist/npm" -name '*.tgz' | wc -l | tr -d ' ')" == "5" ]]
first_digests="$work/first-digests"
(cd "$dist/npm" && sha256_manifest */*.tgz | sort -k2) > "$first_digests"
for package in \
  @nebutra+carina \
  @nebutra+carina-darwin-arm64 \
  @nebutra+carina-darwin-x64 \
  @nebutra+carina-linux-arm64 \
  @nebutra+carina-linux-x64; do
  [[ -f "$dist/npm/$package/package.json" ]]
  tarball="$(find "$dist/npm/$package" -maxdepth 1 -name '*.tgz' -print -quit)"
  if tar -tzf "$tarball" | grep -Fq provenance.json; then
    echo "test-package-npm-release: package contains self-asserted provenance" >&2
    exit 1
  fi
  if [[ "$package" != "@nebutra+carina" ]]; then
    [[ "$(tar -tzf "$tarball" | grep -Ec '^package/bin/(carina|headroom)')" == "11" ]] || {
      echo "test-package-npm-release: $package does not contain the complete native toolchain" >&2
      exit 1
    }
  fi
done

platform="$(node -p 'process.platform')"
arch="$(node -p 'process.arch')"
platform_tarball="$dist/npm/@nebutra+carina-${platform}-${arch}/nebutra-carina-${platform}-${arch}-${version}.tgz"
launcher_tarball="$dist/npm/@nebutra+carina/nebutra-carina-${version}.tgz"
[[ -f "$platform_tarball" && -f "$launcher_tarball" ]]
npm install --global --prefix "$work/global" --ignore-scripts --offline \
  "$platform_tarball" "$launcher_tarball" >/dev/null
for command in carina carina-daemon carina-worker; do
  "$work/global/bin/$command" --version >/dev/null
done

bundle="$dist/carina_${version}_npm_packages.tar.gz"
(cd "$ROOT" && DIST_DIR="$dist" VERSION="$version" ./scripts/package-npm-bundle.sh >/dev/null)
first_bundle_digest="$(sha256_manifest "$bundle" | awk '{print $1}')"
(cd "$ROOT" && DIST_DIR="$dist" VERSION="$version" ./scripts/package-npm-bundle.sh >/dev/null)
[[ "$(sha256_manifest "$bundle" | awk '{print $1}')" == "$first_bundle_digest" ]]
(cd "$ROOT" && VERSION="$version" BUNDLE="$bundle" OUTPUT_DIR="$work/frozen" ./scripts/verify-npm-release-bundle.sh >/dev/null)
[[ "$(find "$work/frozen" -type f -name '*.tgz' | wc -l | tr -d ' ')" == "5" ]]
cp "$bundle" "$work/corrupt-bundle.tar.gz"
printf '\0' | dd of="$work/corrupt-bundle.tar.gz" bs=1 seek=32 count=1 conv=notrunc 2>/dev/null
if (cd "$ROOT" && VERSION="$version" BUNDLE="$work/corrupt-bundle.tar.gz" ./scripts/verify-npm-release-bundle.sh) >/dev/null 2>&1; then
  echo "test-package-npm-release: corrupt frozen bundle was accepted" >&2
  exit 1
fi

(cd "$ROOT" && PATH="$work/bin:$PATH" SYFT_COUNTER_FILE="$counter" DIST_DIR="$dist" VERSION="$version" ./scripts/package-npm-release.sh)
second_digests="$work/second-digests"
(cd "$dist/npm" && sha256_manifest */*.tgz | sort -k2) > "$second_digests"
cmp "$first_digests" "$second_digests" || {
  echo "test-package-npm-release: repeated assembly changed npm tarball bytes" >&2
  exit 1
}

rm "$dist/carina_${version}_linux_arm64.tar.gz"
if (cd "$ROOT" && PATH="$work/bin:$PATH" SYFT_COUNTER_FILE="$counter" DIST_DIR="$dist" VERSION="$version" ./scripts/package-npm-release.sh) >/dev/null 2>&1; then
  echo "test-package-npm-release: missing platform archive was accepted" >&2
  exit 1
fi
echo "test-package-npm-release: ok"
