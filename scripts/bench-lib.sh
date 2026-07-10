#!/usr/bin/env bash

# Shared, workload-free helpers for benchmark gates.

bench_positive_int() {
  if ! [[ "$2" =~ ^[1-9][0-9]*$ ]]; then
    printf 'invalid %s: expected a positive integer, got %s\n' "$1" "${2:-<empty>}" >&2
    return 2
  fi
}

bench_nonnegative_int() {
  if ! [[ "$2" =~ ^(0|[1-9][0-9]*)$ ]]; then
    printf 'invalid %s: expected a non-negative integer, got %s\n' "$1" "${2:-<empty>}" >&2
    return 2
  fi
}

bench_threshold() {
  local env_name="$1"
  local default_value="$2"
  local value="${!env_name:-$default_value}"

  bench_positive_int "$env_name" "$value" || return
  printf '%s\n' "$value"
}

# A target written as "< N ms" fails at N as well as above N.
bench_under_limit() {
  local actual_ms="$1"
  local limit_ms="$2"

  bench_nonnegative_int actual_ms "$actual_ms" || return
  bench_positive_int limit_ms "$limit_ms" || return
  (( actual_ms < limit_ms ))
}
