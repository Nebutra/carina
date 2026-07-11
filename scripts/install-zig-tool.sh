#!/usr/bin/env bash
set -euo pipefail

version="0.15.1"
cache_home="${XDG_CACHE_HOME:-${HOME:-${TMPDIR:-/tmp}}/.cache}"
root="${CARINA_TOOL_CACHE:-$cache_home/carina/tools}"
case "$(uname -s)-$(uname -m)" in
  Darwin-arm64)
    target="aarch64-macos"
    checksum="c4bd624d901c1268f2deb9d8eb2d86a2f8b97bafa3f118025344242da2c54d7b"
    ;;
  Darwin-x86_64)
    target="x86_64-macos"
    checksum="9919392e0287cccc106dfbcbb46c7c1c3fa05d919567bb58d7eb16bca4116184"
    ;;
  Linux-x86_64)
    target="x86_64-linux"
    checksum="c61c5da6edeea14ca51ecd5e4520c6f4189ef5250383db33d01848293bfafe05"
    ;;
  Linux-aarch64|Linux-arm64)
    target="aarch64-linux"
    checksum="bb4a8d2ad735e7fba764c497ddf4243cb129fece4148da3222a7046d3f1f19fe"
    ;;
  *) echo "install-zig-tool: unsupported host $(uname -s)/$(uname -m)" >&2; exit 1 ;;
esac
install="$root/zig-${target}-${version}"
if [[ -x "$install/zig" && "$($install/zig version)" == "$version" ]]; then
  printf '%s\n' "$install/zig"
  exit 0
fi
mkdir -p "$root"
lock="$install.lock"
acquired=0
for _ in {1..300}; do
  if mkdir "$lock" 2>/dev/null; then
    acquired=1
    break
  fi
  if [[ -x "$install/zig" && "$($install/zig version)" == "$version" ]]; then
    printf '%s\n' "$install/zig"
    exit 0
  fi
  sleep 0.1
done
[[ "$acquired" == "1" ]] || { echo "install-zig-tool: timed out waiting for install lock" >&2; exit 1; }
tmp="$(mktemp -d "$root/.zig-${target}-${version}.XXXXXX")"
cleanup() { rm -rf "$tmp" "$lock"; }
trap cleanup EXIT
if [[ -x "$install/zig" && "$($install/zig version)" == "$version" ]]; then
  printf '%s\n' "$install/zig"
  exit 0
fi
archive="$tmp/zig.tar.xz"
url="https://ziglang.org/download/${version}/zig-${target}-${version}.tar.xz"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$url" -o "$archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$archive" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$archive" | awk '{print $1}')"
fi
[[ "$actual" == "$checksum" ]] || { echo "install-zig-tool: checksum mismatch for ${target}" >&2; exit 1; }
mkdir -p "$tmp/extracted"
tar -xJf "$archive" --strip-components=1 -C "$tmp/extracted"
[[ -x "$tmp/extracted/zig" ]] || { echo "install-zig-tool: archive has no executable zig" >&2; exit 1; }
[[ "$($tmp/extracted/zig version)" == "$version" ]] || { echo "install-zig-tool: installed version mismatch" >&2; exit 1; }
rm -rf "$install"
mv "$tmp/extracted" "$install"
printf '%s\n' "$install/zig"
