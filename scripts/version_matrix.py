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
    )
    for relative, pattern, label in regex_owners:
        require_match(root / relative, pattern, version, label)

    web = json.loads((root / "integrations/web/package.json").read_text(encoding="utf-8"))
    require_equal("web package", web["version"], version)
    web_lock = json.loads((root / "integrations/web/package-lock.json").read_text(encoding="utf-8"))
    require_equal("web npm lock", web_lock["version"], version)
    require_equal("web npm lock root", web_lock["packages"][""]["version"], version)

    tauri = json.loads((root / "integrations/tauri/package.json").read_text(encoding="utf-8"))
    require_equal("Tauri package", tauri["version"], version)
    tauri_lock = json.loads((root / "integrations/tauri/package-lock.json").read_text(encoding="utf-8"))
    require_equal("Tauri npm lock", tauri_lock["version"], version)
    require_equal("Tauri npm lock root", tauri_lock["packages"][""]["version"], version)
    tauri_config = json.loads(
        (root / "integrations/tauri/src-tauri/tauri.conf.json").read_text(encoding="utf-8")
    )
    require_equal("Tauri config", tauri_config["version"], version)
    require_match(
        root / "integrations/tauri/src-tauri/Cargo.toml",
        r'(?ms)^\[package\].*?^version = "([^"]+)"',
        version,
        "Tauri Cargo package",
    )
    cargo_lock = (root / "integrations/tauri/src-tauri/Cargo.lock").read_text(encoding="utf-8")
    root_versions = re.findall(
        r'(?ms)^\[\[package\]\]\s+name = "carina-harness"\s+version = "([^"]+)"',
        cargo_lock,
    )
    if len(root_versions) != 1:
        raise SystemExit(
            "version-matrix: expected exactly one carina-harness package in Tauri Cargo.lock"
        )
    require_equal("Tauri Cargo lock root", root_versions[0], version)

    return version


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    args = parser.parse_args()
    version = validate(args.root.resolve())
    print(f"version-matrix: product consumers agree on {version}")


if __name__ == "__main__":
    main()
