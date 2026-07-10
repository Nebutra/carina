#!/usr/bin/env python3
"""Measure command startup/runtime latency and print the median milliseconds."""

import argparse
import statistics
import subprocess
import time


def summarize_ms(timings: list[float]) -> tuple[int, int]:
    return round(timings[0]), round(statistics.median(timings))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--warmups", type=int, default=0)
    parser.add_argument("--samples", type=int, default=1)
    parser.add_argument("--stdin-file")
    parser.add_argument("--summary", action="store_true")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()

    if args.command[:1] == ["--"]:
        args.command = args.command[1:]
    if not args.command:
        parser.error("a command is required after --")
    if args.warmups < 0 or args.samples < 1:
        parser.error("warmups must be non-negative and samples must be positive")

    def run_once() -> float:
        stdin = open(args.stdin_file, "rb") if args.stdin_file else subprocess.DEVNULL
        try:
            started = time.perf_counter()
            subprocess.run(
                args.command,
                check=True,
                stdin=stdin,
                stdout=subprocess.DEVNULL,
            )
            return (time.perf_counter() - started) * 1000
        finally:
            if args.stdin_file:
                stdin.close()

    for _ in range(args.warmups):
        run_once()
    timings = [run_once() for _ in range(args.samples)]
    first_ms, median_ms = summarize_ms(timings)
    if args.summary:
        print(first_ms, median_ms)
    else:
        print(median_ms)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
