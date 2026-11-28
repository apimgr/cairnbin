#!/usr/bin/env bash
# @@License : WTFPL
# Basic integration test runner. The basic tier is Docker-only; full-app,
# clean-OS, and browser tiers are explicit scripts.
# Usage: ./tests/run_tests.sh
set -euo pipefail

RUN_TESTS_SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
if ! command -v docker >/dev/null 2>&1; then
  printf 'ERROR: docker not found: every test script builds with Docker\n' >&2
  printf 'Install Docker: https://docker.com/\n' >&2
  exit 69
fi

exec bash "${RUN_TESTS_SCRIPT_DIR}/docker.sh" "$@"
