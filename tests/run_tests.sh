#!/usr/bin/env bash
# @@License : WTFPL
# Basic integration test runner. The basic tier is Docker-only; full-app,
# clean-OS, and browser tiers are explicit scripts.
# Usage: ./tests/run_tests.sh [--keep]
set -euo pipefail

RUN_TESTS_SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
exec bash "${RUN_TESTS_SCRIPT_DIR}/docker.sh" "$@"
