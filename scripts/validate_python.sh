#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
UV_VERSION=0.12.13
UV_SHA256_X64=745765a3b6e360ad76743599ae5c42e9278c7edf8bbff9fc76d05bf2623a04dd
UV_SHA256_ARM64=2eaa5d94f5db7b3a1a092156b9420459e42ab0217d917fe74a876309cef9b5e9
UV_HOME=""
PYTHON_ENV=$(mktemp -d "${TMPDIR:-/tmp}/gds-python.XXXXXX")

cleanup() {
  rm -rf -- "$PYTHON_ENV"
  [ -n "$UV_HOME" ] && rm -rf -- "$UV_HOME"
  return 0
}
trap cleanup EXIT INT TERM

uv_binary=$(command -v uv 2>/dev/null || true)
if [ -z "$uv_binary" ] || [ "$("$uv_binary" --version 2>/dev/null | awk '{print $2}')" != "$UV_VERSION" ]; then
  [ "$(uname -s)" = Linux ] || {
    printf 'uv %s is required on this platform\n' "$UV_VERSION" >&2
    exit 1
  }
  case "$(uname -m)" in
    x86_64 | amd64) arch=x86_64; digest=$UV_SHA256_X64 ;;
    aarch64 | arm64) arch=aarch64; digest=$UV_SHA256_ARM64 ;;
    *) printf 'no pinned uv artifact for %s\n' "$(uname -m)" >&2; exit 1 ;;
  esac
  UV_HOME=$(mktemp -d "${TMPDIR:-/tmp}/gds-uv.XXXXXX")
  curl -fsSL \
    "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-${arch}-unknown-linux-gnu.tar.gz" \
    -o "$UV_HOME/uv.tar.gz"
  printf '%s  %s\n' "$digest" "$UV_HOME/uv.tar.gz" | sha256sum --check --status
  tar -xzf "$UV_HOME/uv.tar.gz" --strip-components=1 -C "$UV_HOME"
  uv_binary=$UV_HOME/uv
fi

cd "$ROOT"
"$uv_binary" venv --python 3.14.7 "$PYTHON_ENV"
"$uv_binary" pip install --python "$PYTHON_ENV/bin/python" \
  --require-hashes -r requirements/test.txt
"$PYTHON_ENV/bin/python" -m pytest
