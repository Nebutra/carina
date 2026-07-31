#!/usr/bin/env python3
"""Validate product-version consumers against the Go product authority."""

from __future__ import annotations

import argparse
import json
import re
from pathlib import Path


def require_equal(label: str, actual: str, expected: str) -> None:
    if actual != expected:
        raise SystemExit(f"version-matrix: {label}={actual!r} want={expected!r}")


def require_match(path: Path, pattern: str, expected: str, label: str) -> None:
    text = path.read_text(encoding="utf-8")
    match = re.search(pattern, text)
    if match is None:
        raise SystemExit(f"version-matrix: cannot read {label} from {path}")
    require_equal(label, match.group(1), expected)


def validate(root: Path) -> str:
    product_source = (root / "go/product/version.go").read_text(encoding="utf-8")
    product_match = re.search(r'const Version = "([^"]+)"', product_source)
    if product_match is None:
        raise SystemExit("version-matrix: cannot read go/product/version.go")
    version = product_match.group(1)

    require_match(
        root / "crates/carina-tui/Cargo.toml",
        r'(?ms)^\[package\].*?^version = "([^"]+)"',
        version,
        "carina-tui",
    )

    npm = json.loads((root / "packaging/npm/package.json").read_text(encoding="utf-8"))
    require_equal("npm launcher", npm["version"], version)
    for package, dependency_version in npm["optionalDependencies"].items():
        require_equal(f"npm optional dependency {package}", dependency_version, version)

    vscode = json.loads((root / "integrations/vscode/package.json").read_text(encoding="utf-8"))
    require_equal("VS Code package", vscode["version"], version)
    vscode_lock = json.loads((root / "integrations/vscode/package-lock.json").read_text(encoding="utf-8"))
    require_equal("VS Code lock", vscode_lock["version"], version)
    require_equal("VS Code lock root", vscode_lock["packages"][""]["version"], version)

    regex_owners = (
        ("sdk/go/client.go", r'const CompatibleRuntimeVersion = "([^"]+)"', "Go SDK"),
        ("sdk/typescript/src/index.ts", r"compatibleRuntimeVersion = '([^']+)'", "TypeScript SDK"),
        ("sdk/python/src/carina_sdk/__init__.py", r'compatible_runtime_version = "([^"]+)"', "Python SDK"),
        ("integrations/vscode/src/extension.ts", r"client_version:'([^']+)'", "VS Code client"),
        ("integrations/web/app.js", r"client_version:'([^']+)'", "web client"),
    )
    for relative, pattern, label in regex_owners:
        require_match(root / relative, pattern, version, label)

    return version


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    args = parser.parse_args()
    version = validate(args.root.resolve())
    print(f"version-matrix: product consumers agree on {version}")


if __name__ == "__main__":
    main()
