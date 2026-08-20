#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
TIER=${1:-}
if [ "$#" -ne 1 ]; then
  printf 'usage: %s fast|pr-required|full|release\n' "$0" >&2
  exit 4
fi

cd "$ROOT"
export GOTOOLCHAIN=${GOTOOLCHAIN:-go1.26.5}
export GOWORK=off
export GOFLAGS=-mod=readonly

run_python_contracts() {
  python3 scripts/validate_gds_schemas.py
  python3 scripts/validate_python_locks.py
  python3 scripts/generate_adr_index.py --check
}

run_python_tests() {
  local python=${GDS_TEST_PYTHON:-python3}
  if [ -z "${GDS_TEST_PYTHON:-}" ] && [ -x .venv/bin/python ] &&
    .venv/bin/python -c 'import pytest' 2>/dev/null; then
    python=.venv/bin/python
  fi
  "$python" -m pytest
}

case "$TIER" in
  fast)
    scripts/validate_shell.sh
    scripts/validate_go_core.sh --quick
    run_python_contracts
    ;;
  pr-required)
    scripts/validate_shell.sh
    scripts/validate_go_core.sh
    run_python_contracts
    run_python_tests
    scripts/validate_assurance.sh --skip-tests
    ;;
  full)
    scripts/validate_shell.sh
    scripts/validate_go_core.sh
    run_python_contracts
    run_python_tests
    scripts/validate_assurance.sh
    ;;
  release)
    scripts/validate_release.sh
    ;;
  *)
    printf 'unknown CI tier: %s\n' "$TIER" >&2
    exit 4
    ;;
esac
