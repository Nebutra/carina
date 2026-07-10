#!/usr/bin/env bash
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
source "$ROOT/scripts/bench-lib.sh"

failures=0

expect_pass() {
  local name="$1"
  shift
  if ! "$@"; then
    echo "FAIL: $name" >&2
    failures=$((failures + 1))
  fi
}

expect_fail() {
  local name="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    echo "FAIL: $name" >&2
    failures=$((failures + 1))
  fi
}

expect_equal() {
  local name="$1"
  local want="$2"
  local got="$3"
  if [ "$want" != "$got" ]; then
    echo "FAIL: $name: want $want, got $got" >&2
    failures=$((failures + 1))
  fi
}

expect_pass "below threshold passes" bench_under_limit 299 300
expect_fail "threshold boundary fails" bench_under_limit 300 300
expect_fail "above threshold fails" bench_under_limit 301 300
expect_pass "zero measurement passes" bench_under_limit 0 300
expect_fail "non-integer measurement is rejected" bench_under_limit 1.5 300

unset CARINA_BENCH_TEST_THRESHOLD_MS
expect_equal "default threshold" 300 "$(bench_threshold CARINA_BENCH_TEST_THRESHOLD_MS 300)"
CARINA_BENCH_TEST_THRESHOLD_MS=450
expect_equal "environment override" 450 "$(bench_threshold CARINA_BENCH_TEST_THRESHOLD_MS 300)"
CARINA_BENCH_TEST_THRESHOLD_MS=invalid
expect_fail "invalid environment override is rejected" bench_threshold CARINA_BENCH_TEST_THRESHOLD_MS 300

if [ "$failures" -ne 0 ]; then
  echo "BENCH GATE TESTS: $failures failed" >&2
  exit 1
fi

python3 "$ROOT/scripts/test-bench-helpers.py"
echo "BENCH GATE TESTS OK"
