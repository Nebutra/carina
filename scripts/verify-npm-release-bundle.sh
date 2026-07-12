#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:?VERSION is required}"
BUNDLE="${BUNDLE:-$ROOT/dist/carina_${VERSION}_npm_packages.tar.gz}"
OUTPUT_DIR="${OUTPUT_DIR:-}"
expected=(
  "nebutra-carina-${VERSION}.tgz"
  "nebutra-carina-darwin-arm64-${VERSION}.tgz"
  "nebutra-carina-darwin-x64-${VERSION}.tgz"
  "nebutra-carina-linux-arm64-${VERSION}.tgz"
  "nebutra-carina-linux-x64-${VERSION}.tgz"
)
names=(
  "@nebutra/carina"
  "@nebutra/carina-darwin-arm64"
  "@nebutra/carina-darwin-x64"
  "@nebutra/carina-linux-arm64"
  "@nebutra/carina-linux-x64"
)

[[ -f "$BUNDLE" ]] || { echo "verify-npm-release-bundle: missing $BUNDLE" >&2; exit 1; }
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-npm-bundle-verify.XXXXXX")"
trap 'rm -rf "$work"' EXIT
listing="$work/listing"
tar -tzf "$BUNDLE" | LC_ALL=C sort > "$listing"
{
  echo npm/SHA256SUMS
  for name in "${expected[@]}"; do echo "npm/$name"; done
} | LC_ALL=C sort > "$work/expected-listing"
cmp "$work/expected-listing" "$listing" || {
  echo "verify-npm-release-bundle: unexpected bundle contents" >&2
  exit 1
}
tar -xzf "$BUNDLE" -C "$work"
(cd "$work/npm" && shasum -a 256 -c SHA256SUMS >/dev/null)

for index in "${!expected[@]}"; do
  tarball="$work/npm/${expected[$index]}"
  metadata="$(tar -xOf "$tarball" package/package.json)"
  node -e '
    const data = JSON.parse(process.argv[1]);
    if (data.name !== process.argv[2] || data.version !== process.argv[3]) process.exit(1);
  ' "$metadata" "${names[$index]}" "$VERSION" || {
    echo "verify-npm-release-bundle: package identity mismatch in ${expected[$index]}" >&2
    exit 1
  }
done

if [[ -n "$OUTPUT_DIR" ]]; then
  mkdir -p "$OUTPUT_DIR"
  rm -f "$OUTPUT_DIR"/*.tgz
  cp "$work/npm"/*.tgz "$OUTPUT_DIR/"
fi
echo "verify-npm-release-bundle: $VERSION ok"
