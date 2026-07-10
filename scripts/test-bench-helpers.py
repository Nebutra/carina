#!/usr/bin/env python3

import importlib.util
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parent


def load_script(name: str, filename: str):
    spec = importlib.util.spec_from_file_location(name, SCRIPTS / filename)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


generator = load_script("bench_generator", "generate-bench-repo.py")
measurement = load_script("bench_measurement", "measure-command-ms.py")


class WorkloadTests(unittest.TestCase):
    def test_sparse_fixed_hit_rate(self):
        with tempfile.TemporaryDirectory() as raw_root:
            root = Path(raw_root)
            hits = generator.generate(
                root, file_count=250, grep_file_count=150, match_every=100
            )
            files = list(root.rglob("*.go"))
            medium_files = list((root / "medium").rglob("*.go"))

            self.assertEqual(len(files), 250)
            self.assertEqual(len(medium_files), 150)
            self.assertEqual(hits, 2)
            self.assertEqual(
                sum(
                    generator.NEEDLE in path.read_text(encoding="utf-8")
                    for path in medium_files
                ),
                2,
            )


class SamplingTests(unittest.TestCase):
    def test_reports_first_sample_and_odd_median(self):
        self.assertEqual(measurement.summarize_ms([91.2, 12.0, 20.4]), (91, 20))

    def test_even_sample_median_is_supported(self):
        self.assertEqual(measurement.summarize_ms([40.0, 10.0, 20.0, 30.0]), (40, 25))


if __name__ == "__main__":
    unittest.main()
