#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

python3 - <<'PY'
import re
from pathlib import Path

# This is a source-contract test, not the Python 3.12 release builder. Keep it
# runnable on developer machines that have an older system Python; the builder
# itself still enforces the pinned 3.12 interpreter before parsing TOML.
text = Path("integrations/headroom.lock").read_text()

def scalar(name):
    match = re.search(rf'^{re.escape(name)} = "([^"]+)"$', text, re.M)
    assert match, f"missing {name}"
    return match.group(1)

tag = scalar("upstream_tag")
assert scalar("python_version") == "3.12"
assert scalar("pyinstaller_version") == "6.21.0"

targets = {"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64"}
sections = dict(re.findall(r'^\[artifacts\.([^\]]+)\]\n(.*?)(?=^\[|\Z)', text, re.M | re.S))
assert set(sections) == targets
for target, section in sections.items():
    values = dict(re.findall(r'^(source_url|source_sha256|bundle_path) = "([^"]+)"$', section, re.M))
    assert values["source_url"].startswith(
        f"https://github.com/headroomlabs-ai/headroom/releases/download/{tag}/"
    )
    assert re.fullmatch(r"[0-9a-f]{64}", values["source_sha256"])
    assert values["bundle_path"] == f"dist/headroom/{target}/headroom"
assert dict(re.findall(r'^(source_url|source_sha256|bundle_path) = "([^"]+)"$', sections["darwin-amd64"], re.M))["source_url"].endswith(".tar.gz")

requirements = Path("integrations/headroom-requirements.lock").read_text()
for package in ("maturin==1.14.1", "pyinstaller==6.21.0"):
    assert package in requirements
assert "--hash=sha256:" in requirements
PY

if rg -n 'SKIP_HEADROOM:\s*"1"' .github/workflows/release.yml; then
  echo "test-headroom-build-contract: release workflow still skips Headroom" >&2
  exit 1
fi

bash -n scripts/build-headroom-bundle.sh scripts/update-headroom-requirements.sh
python3 -m py_compile scripts/verify-headroom-bundle.py
echo "test-headroom-build-contract: ok"
