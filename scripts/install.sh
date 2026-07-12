#!/usr/bin/env sh
set -eu

repo="${CARINA_INSTALL_REPOSITORY:-Nebutra/carina}"
version="${VERSION:-}"
os="${CARINA_INSTALL_OS:-$(uname -s)}"
machine="${CARINA_INSTALL_ARCH:-$(uname -m)}"
bin_dir="${BIN_DIR:-${HOME}/.local/bin}"

case "$os" in
  Darwin|darwin) target_os=darwin ;;
  Linux|linux) target_os=linux ;;
  *) echo "carina install: unsupported operating system: $os" >&2; exit 2 ;;
esac
case "$machine" in
  arm64|aarch64) target_arch=arm64 ;;
  x86_64|amd64) target_arch=amd64 ;;
  *) echo "carina install: unsupported architecture: $machine" >&2; exit 2 ;;
esac

if [ -z "$version" ]; then
  version="$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p' \
    | head -n 1)"
  [ -n "$version" ] || { echo "carina install: cannot resolve latest release" >&2; exit 1; }
fi

archive="carina_${version}_${target_os}_${target_arch}.tar.gz"
base="${CARINA_RELEASE_BASE_URL:-https://github.com/$repo/releases/download/v$version}"
work="$(mktemp -d "${TMPDIR:-/tmp}/carina-install.XXXXXX")"
trap 'rm -rf "$work"' EXIT HUP INT TERM
curl -fsSL "$base/$archive" -o "$work/$archive"
curl -fsSL "$base/$archive.sha256" -o "$work/$archive.sha256"

set -- $(cat "$work/$archive.sha256")
expected="${1:-}"
filename="${2:-}"
[ "$filename" = "$archive" ] || { echo "carina install: checksum filename mismatch" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$work/$archive" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$work/$archive" | awk '{print $1}')"
fi
[ "$actual" = "$expected" ] || { echo "carina install: checksum mismatch" >&2; exit 1; }

mkdir -p "$work/extract" "$bin_dir"
tar -xzf "$work/$archive" -C "$work/extract"
root="$(find "$work/extract" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
[ -n "$root" ] && [ -d "$root/bin" ] || { echo "carina install: invalid archive layout" >&2; exit 1; }
for binary in "$root"/bin/*; do
  [ -f "$binary" ] && [ -x "$binary" ] || continue
  cp "$binary" "$bin_dir/$(basename "$binary")"
  chmod 0755 "$bin_dir/$(basename "$binary")"
done
[ -x "$bin_dir/carina" ] || { echo "carina install: archive did not contain carina" >&2; exit 1; }
echo "carina install: installed $version to $bin_dir"
