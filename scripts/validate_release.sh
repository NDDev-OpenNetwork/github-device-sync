#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
BUILD_DIR=$(mktemp -d "${TMPDIR:-/tmp}/gds-release-gate.XXXXXX")
trap 'rm -rf -- "$BUILD_DIR"' EXIT INT TERM

cd "$ROOT"
export GOTOOLCHAIN=${GOTOOLCHAIN:-go1.26.7}
export GOWORK=off
export GOFLAGS=-mod=readonly

scripts/validate_ci_tier.sh full
go build -trimpath -o "$BUILD_DIR/gds" ./core/cmd/gds

run_validator() {
  local output=$1
  shift
  local status
  if "$@" >"$output"; then
    return 0
  else
    status=$?
  fi
  cat "$output" >&2
  return "$status"
}

run_validator "$BUILD_DIR/context.json" \
  env XDG_CONFIG_HOME="$BUILD_DIR/config" "$BUILD_DIR/gds" --json context
for CONTRACT in source-freshness visibility absolute-paths public-artifact; do
  run_validator "$BUILD_DIR/validate-$CONTRACT.json" \
    "$BUILD_DIR/gds" --json validate "$CONTRACT"
done
for HARNESS in claude-code codex grok-build opencode pi; do
  run_validator "$BUILD_DIR/validate-harness-$HARNESS.json" \
    "$BUILD_DIR/gds" --json validate harnesses --harness "$HARNESS" --runtime
done

printf 'GDS release validation: PASS\n'
