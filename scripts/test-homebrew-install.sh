#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
ARCHIVE="${ARCHIVE:-$ROOT/dist/carina_${VERSION}_darwin_${GOARCH}.tar.gz}"
TAP="carina/release-test"

if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  printf 'test-homebrew-install: VERSION is required\n' >&2
  exit 2
fi
if [[ ! -f "$ARCHIVE" ]]; then
  printf 'test-homebrew-install: archive not found: %s\n' "$ARCHIVE" >&2
  exit 1
fi

installed=0
tapped=0
cleanup() {
  if [[ "$installed" == "1" ]]; then
    HOMEBREW_NO_AUTO_UPDATE=1 brew uninstall --formula carina >/dev/null 2>&1 || true
  fi
  if [[ "$tapped" == "1" ]]; then
    HOMEBREW_NO_AUTO_UPDATE=1 brew untap "$TAP" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

HOMEBREW_NO_AUTO_UPDATE=1 brew tap-new --no-git "$TAP" >/dev/null
tapped=1
tap_root="$(brew --repository "$TAP")"
archive_dir="$(cd "$(dirname "$ARCHIVE")" && pwd)"
sha256="$(shasum -a 256 "$ARCHIVE" | awk '{print $1}')"
darwin_arm64_sha256="$(printf 'a%.0s' {1..64})"
darwin_amd64_sha256="$(printf 'b%.0s' {1..64})"
case "$GOARCH" in
  arm64) darwin_arm64_sha256="$sha256" ;;
  amd64) darwin_amd64_sha256="$sha256" ;;
  *)
    printf 'test-homebrew-install: unsupported GOARCH: %s\n' "$GOARCH" >&2
    exit 2
    ;;
esac

VERSION="$VERSION" \
DARWIN_ARM64_SHA256="$darwin_arm64_sha256" \
DARWIN_AMD64_SHA256="$darwin_amd64_sha256" \
RELEASE_BASE_URL="file://$archive_dir" \
OUTPUT="$tap_root/Formula/carina.rb" \
  "$ROOT/scripts/render-homebrew-formula.sh"

HOMEBREW_NO_AUTO_UPDATE=1 brew style "$tap_root/Formula/carina.rb"
HOMEBREW_NO_AUTO_UPDATE=1 brew install --formula "$TAP/carina"
installed=1
HOMEBREW_NO_AUTO_UPDATE=1 brew test "$TAP/carina"

prefix="$(brew --prefix carina)"
if [[ "$("$prefix/bin/carina" --version)" != "carina $VERSION" ]]; then
  printf 'test-homebrew-install: installed CLI version mismatch\n' >&2
  exit 1
fi

for executable in \
  carina \
  carina-daemon \
  carina-worker \
  carina-tui \
  carina-kernel-service \
  carina-scan \
  carina-grep \
  carina-diff \
  carina-run \
  carina-pty \
  carina-patch-native; do
  if [[ ! -x "$prefix/bin/$executable" ]]; then
    printf 'test-homebrew-install: missing executable: %s\n' "$executable" >&2
    exit 1
  fi
done

printf 'test-homebrew-install: ok\n'
