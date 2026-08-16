#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
MODE=full
MINIMUM_SECURE_GO_VERSION=go1.26.5
RELEASE_GO_VERSION=${GDS_RELEASE_GO_VERSION:-go1.26.5}

if [ "${1:-}" = "--quick" ]; then
  MODE=quick
  shift
fi
if [ "$#" -ne 0 ]; then
  printf 'usage: %s [--quick]\n' "$0" >&2
  exit 4
fi

cd "$ROOT"
export GOWORK=off
export GOFLAGS=-mod=readonly

GO_VERSION=$(go env GOVERSION)
go_version_at_least() {
  python3 - "$1" "$2" <<'PY'
import re
import sys


def parse(value: str) -> tuple[int, int, int]:
    match = re.fullmatch(r"go(\d+)\.(\d+)\.(\d+)", value)
    if match is None:
        raise ValueError(value)
    return tuple(int(part) for part in match.groups())


try:
    current = parse(sys.argv[1])
    minimum = parse(sys.argv[2])
except ValueError as error:
    print(f"unsupported Go version string: {error.args[0]}", file=sys.stderr)
    raise SystemExit(4)

raise SystemExit(0 if current >= minimum else 1)
PY
}

if ! go_version_at_least "$GO_VERSION" "$MINIMUM_SECURE_GO_VERSION"; then
  if [ "$MODE" = "full" ]; then
    printf '%s\n' \
      "GDS release validation blocked: $GO_VERSION is older than the security floor $MINIMUM_SECURE_GO_VERSION." \
      "Upgrade the trusted Go builder and rerun the full gate; no release evidence was produced." >&2
    exit 13
  fi
  printf '%s\n' \
    "WARNING: $GO_VERSION is older than the security floor $MINIMUM_SECURE_GO_VERSION." \
    "Quick results are development-only; release evidence remains NOT_PROVEN." >&2
fi

if [ "$MODE" = "full" ] && [ "$GO_VERSION" != "$RELEASE_GO_VERSION" ]; then
  printf '%s\n' \
    "GDS release validation blocked: expected exact builder $RELEASE_GO_VERSION, found $GO_VERSION." \
    "Update the pinned source register before accepting a different release builder." >&2
  exit 13
fi

BUILD_DIR=$(mktemp -d "${TMPDIR:-/tmp}/gds-build.XXXXXX")
trap 'rm -rf -- "$BUILD_DIR"' EXIT INT TERM

UNFORMATTED=$(gofmt -l core schemas/embed.go)
if [ -n "$UNFORMATTED" ]; then
  printf 'gofmt required for:\n%s\n' "$UNFORMATTED" >&2
  exit 2
fi

go mod tidy -diff
go mod verify
go vet ./...
go test ./...
python3 scripts/validate_python_locks.py
python3 scripts/validate_gds_schemas.py --json >/dev/null

GDS_BIN="$BUILD_DIR/gds-host"
go build -trimpath -o "$GDS_BIN" ./core/cmd/gds
for CONTRACT in schemas repository estate skills plugins memories; do
  "$GDS_BIN" --json validate "$CONTRACT" \
    >"$BUILD_DIR/validate-$CONTRACT.json"
done
"$GDS_BIN" --json generate repository --check >"$BUILD_DIR/generate-repository.json"

if [ "$MODE" = "full" ]; then
  go test -race ./...
  mkdir -p "$BUILD_DIR/cross"
  for TARGET in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
    GOOS=${TARGET%/*}
    GOARCH=${TARGET#*/}
    OUTPUT="$BUILD_DIR/cross/gds-$GOOS-$GOARCH"
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
      go build -trimpath -o "$OUTPUT" ./core/cmd/gds
  done
fi

printf 'GDS Go core validation: PASS (%s)\n' "$MODE"
