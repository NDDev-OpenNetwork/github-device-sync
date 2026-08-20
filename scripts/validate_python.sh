#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
UV_VERSION=0.11.30
UV_SHA256_X64=04bc7d180d6138bf6dc08387acf507a823f397a98fea55da36b0ccc7fbce3b68
UV_SHA256_ARM64=8c11d90f5f66d232930cf8ae3a085c39877690d409e10878234802b028b20e2a
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
"$uv_binary" venv --python 3.14.4 "$PYTHON_ENV"
"$uv_binary" pip install --python "$PYTHON_ENV/bin/python" \
  --require-hashes -r requirements/test.txt
"$PYTHON_ENV/bin/python" -m pytest
