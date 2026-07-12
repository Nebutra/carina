#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

python3 - <<'PY'
import re
import tomllib
from pathlib import Path

lock = tomllib.loads(Path("integrations/headroom.lock").read_text())
assert lock["python_version"] == "3.12"
assert lock["pyinstaller_version"] == "6.21.0"
assert set(lock["artifacts"]) == {
    "darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64"
}
for target, artifact in lock["artifacts"].items():
    assert artifact["source_url"].startswith(
        f"https://github.com/headroomlabs-ai/headroom/releases/download/{lock['upstream_tag']}/"
    )
    assert re.fullmatch(r"[0-9a-f]{64}", artifact["source_sha256"])
    assert artifact["bundle_path"] == f"dist/headroom/{target}/headroom"
assert lock["artifacts"]["darwin-amd64"]["source_url"].endswith(".tar.gz")

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
