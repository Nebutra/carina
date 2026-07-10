#!/usr/bin/env python3
"""Generate the deterministic medium-repository benchmark workload."""

import argparse
from pathlib import Path


NEEDLE = "CARINA_BENCH_HIT"
MISS = "CARINA_BENCH_MISS"


def generate(root: Path, file_count: int, grep_file_count: int, match_every: int) -> int:
    if file_count < 1 or grep_file_count < 1 or match_every < 1:
        raise ValueError("file_count, grep_file_count, and match_every must be positive")
    if grep_file_count > file_count:
        raise ValueError("grep_file_count cannot exceed file_count")

    hits = 0
    for index in range(file_count):
        in_medium_repo = index < grep_file_count
        corpus = "medium" if in_medium_repo else "scan-only"
        corpus_index = index if in_medium_repo else index - grep_file_count
        directory = root / corpus / f"pkg{corpus_index // 200}"
        directory.mkdir(parents=True, exist_ok=True)
        marker = NEEDLE if in_medium_repo and index % match_every == 0 else MISS
        if marker == NEEDLE:
            hits += 1
        (directory / f"f{index}.go").write_text(
            f"package p\n// item {index} {marker}\n"
            f"func F{index}() int {{ return {index} }}\n",
            encoding="utf-8",
        )
    return hits


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("root", type=Path)
    parser.add_argument("file_count", type=int)
    parser.add_argument("grep_file_count", type=int)
    parser.add_argument("match_every", type=int)
    args = parser.parse_args()

    hits = generate(args.root, args.file_count, args.grep_file_count, args.match_every)
    print(args.file_count, args.grep_file_count, hits, NEEDLE)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
