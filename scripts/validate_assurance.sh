#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
RUN_TESTS=true

if [ "${1:-}" = "--skip-tests" ]; then
  RUN_TESTS=false
  shift
fi
if [ "$#" -ne 0 ]; then
  printf 'usage: %s [--skip-tests]\n' "$0" >&2
  exit 4
fi

BUILD_DIR=$(mktemp -d "${TMPDIR:-/tmp}/gds-assurance-gate.XXXXXX")
trap 'rm -rf -- "$BUILD_DIR"' EXIT INT TERM

cd "$ROOT"
export GOTOOLCHAIN=${GOTOOLCHAIN:-go1.26.5}
export GOWORK=off
export GOFLAGS=-mod=readonly
if [ "$RUN_TESTS" = true ]; then
  go test -race ./core/...
fi

ASSURANCE_BIN="$BUILD_DIR/gds-assurance"
REPORT="$BUILD_DIR/assurance-report.json"
go build -trimpath -o "$ASSURANCE_BIN" ./core/cmd/gds-assurance
XDG_CONFIG_HOME="$BUILD_DIR/config" "$ASSURANCE_BIN" \
  --performance-mode deterministic-required --root "$ROOT" --output "$REPORT" \
  >"$BUILD_DIR/assurance-stdout.json"

python3 - "$REPORT" <<'PY'
import json
import sys
from pathlib import Path

stable = {
    "peak-heap-bytes",
    "state-db-bytes",
    "api-read-calls-per-full-reconciliation",
}
report = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
expected = {
    "repositories": 2000,
    "installations": 5,
    "forks": 1000,
    "shared_modules": 4,
    "module_consumers": 1000,
    "webhook_deliveries": 1000,
    "lifecycle_classes": 4,
    "access_states": 5,
}
if report["scenario"] != expected:
    raise SystemExit(f"assurance scenario mismatch: {report['scenario']!r}")
if len(report["checks"]) != 16 or any(item["status"] != "pass" for item in report["checks"]):
    raise SystemExit("assurance checks are incomplete")
if len(report["metrics"]) != 13:
    raise SystemExit("assurance metric count is wrong")
for metric in report["metrics"]:
    if metric["id"] in stable and not metric["passed"]:
        raise SystemExit(f"stable budget {metric['id']} did not pass")
PY

printf 'GDS integrated assurance: PASS\n'
