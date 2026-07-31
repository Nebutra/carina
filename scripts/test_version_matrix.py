#!/usr/bin/env python3
"""Regression tests for the product version ownership matrix."""

from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("version_matrix.py")
SPEC = importlib.util.spec_from_file_location("version_matrix", MODULE_PATH)
assert SPEC and SPEC.loader
version_matrix = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(version_matrix)


class VersionMatrixTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self._write("go/product/version.go", 'package product\nconst Version = "1.2.3"\n')
        self._write("crates/carina-tui/Cargo.toml", '[package]\nname = "carina-tui"\nversion = "1.2.3"\n')
        self._write_json(
            "packaging/npm/package.json",
            {"version": "1.2.3", "optionalDependencies": {"@nebutra/carina-test": "1.2.3"}},
        )
        self._write_json("integrations/vscode/package.json", {"version": "1.2.3"})
        self._write_json(
            "integrations/vscode/package-lock.json",
            {"version": "1.2.3", "packages": {"": {"version": "1.2.3"}}},
        )
        self._write("sdk/go/client.go", 'const CompatibleRuntimeVersion = "1.2.3"\n')
        self._write("sdk/typescript/src/index.ts", "export const compatibleRuntimeVersion = '1.2.3'\n")
        self._write("sdk/python/src/carina_sdk/__init__.py", 'compatible_runtime_version = "1.2.3"\n')
        self._write("integrations/vscode/src/extension.ts", "client_version:'1.2.3'\n")
        self._write("integrations/web/app.js", "client_version:'1.2.3'\n")

    def tearDown(self) -> None:
        self.temp.cleanup()

    def _write(self, relative: str, value: str) -> None:
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(value, encoding="utf-8")

    def _write_json(self, relative: str, value: object) -> None:
        self._write(relative, json.dumps(value))

    def test_all_consumers_agree(self) -> None:
        self.assertEqual(version_matrix.validate(self.root), "1.2.3")

    def test_drift_fails_closed(self) -> None:
        owners = (
            "crates/carina-tui/Cargo.toml",
            "packaging/npm/package.json",
            "integrations/vscode/package.json",
            "integrations/vscode/package-lock.json",
            "sdk/go/client.go",
            "sdk/typescript/src/index.ts",
            "sdk/python/src/carina_sdk/__init__.py",
            "integrations/vscode/src/extension.ts",
            "integrations/web/app.js",
        )
        for relative in owners:
            with self.subTest(owner=relative):
                path = self.root / relative
                original = path.read_text(encoding="utf-8")
                path.write_text(original.replace("1.2.3", "9.9.9", 1), encoding="utf-8")
                with self.assertRaises(SystemExit):
                    version_matrix.validate(self.root)
                path.write_text(original, encoding="utf-8")


if __name__ == "__main__":
    unittest.main()
