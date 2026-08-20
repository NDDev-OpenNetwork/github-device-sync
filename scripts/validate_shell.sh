#!/usr/bin/env bash
# Lint every Bash script this repository owns.
#
# The control plane carries ~2500 lines of Bash — the device bootstrap
# orchestrator, the quarantined pre-GDS engine and its parity suite, and the
# validation entrypoints themselves — and nothing checked any of it. The scripts
# even carry `# shellcheck disable=` directives, so the linter was run by hand
# once and never wired into a gate.
#
# That gap was not theoretical. `scripts/bootstrap-device.sh` shipped
# `[ -n "$GDS_REGISTRATION_PATH:-}" ]`: a dropped brace that made the test
# always true and, under `set -u`, aborted phase 3b of every bootstrap on a
# device that had not already been registered — the zero-to-one path, and the
# one place a failure is most expensive. ShellCheck classifies it SC2157, an
# error, and would have rejected it on the first push.
#
# Pinned exactly like the uv bootstrap in validate_ci_tier.sh: the version and
# its per-platform digest are tracked here, the archive is verified before it is
# unpacked, and a mismatched or missing artifact fails the gate instead of
# silently falling through to whatever the host happens to have. A host copy is
# accepted only when it reports the pinned version, because a lane that runs a
# different linter than the one it declares is not evidence.
set -euo pipefail

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

SHELLCHECK_VERSION="0.11.0"
SHELLCHECK_SHA256_LINUX_X86_64="8c3be12b05d5c177a04c29e3c78ce89ac86f1595681cab149b65b97c4e227198"
SHELLCHECK_SHA256_LINUX_AARCH64="12b331c1d2db6b9eb13cfca64306b1b157a86eb69db83023e261eaa7e7c14588"
SHELLCHECK_SHA256_DARWIN_X86_64="3c89db4edcab7cf1c27bff178882e0f6f27f7afdf54e859fa041fca10febe4c6"
SHELLCHECK_SHA256_DARWIN_AARCH64="56affdd8de5527894dca6dc3d7e0a99a873b0f004d7aabc30ae407d3f48b0a79"

DOWNLOAD_HOME=""
SHELLCHECK_BIN=""

cleanup() {
  [ -n "$DOWNLOAD_HOME" ] && rm -rf -- "$DOWNLOAD_HOME"
  return 0
}
trap cleanup EXIT INT TERM

# Every Bash file the repository owns. Listed explicitly rather than globbed so
# that adding a script is a deliberate act that also decides whether it is
# linted; a silent miss is exactly what this gate exists to prevent.
SCRIPTS=(
  "scripts/bootstrap-device.sh"
  "scripts/gds-exact-apply.sh"
  "scripts/validate_assurance.sh"
  "scripts/validate_ci_tier.sh"
  "scripts/validate_go_core.sh"
  "scripts/validate_python.sh"
  "scripts/validate_release.sh"
  "scripts/validate_shell.sh"
)

host_version() {
  command -v shellcheck >/dev/null 2>&1 || return 1
  shellcheck --version 2>/dev/null |
    awk '/^version:/ { print $2; exit }'
}

platform_digest() {
  case "$1" in
    linux.x86_64) printf '%s' "$SHELLCHECK_SHA256_LINUX_X86_64" ;;
    linux.aarch64) printf '%s' "$SHELLCHECK_SHA256_LINUX_AARCH64" ;;
    darwin.x86_64) printf '%s' "$SHELLCHECK_SHA256_DARWIN_X86_64" ;;
    darwin.aarch64) printf '%s' "$SHELLCHECK_SHA256_DARWIN_AARCH64" ;;
    *) return 1 ;;
  esac
}

verify_digest() {
  local expected=$1 path=$2 actual
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$path" | awk '{print $1}')
  else
    actual=$(shasum -a 256 "$path" | awk '{print $1}')
  fi
  [ "$actual" = "$expected" ] || {
    printf 'shellcheck archive digest mismatch: expected %s, got %s\n' \
      "$expected" "$actual" >&2
    exit 1
  }
}

ensure_shellcheck() {
  local found
  found=$(host_version || true)
  if [ "$found" = "$SHELLCHECK_VERSION" ]; then
    SHELLCHECK_BIN=$(command -v shellcheck)
    return
  fi
  if [ -n "$found" ]; then
    printf 'host shellcheck is %s; this gate is pinned to %s, fetching it\n' \
      "$found" "$SHELLCHECK_VERSION" >&2
  fi

  local os arch platform digest
  case "$(uname -s)" in
    Linux) os="linux" ;;
    Darwin) os="darwin" ;;
    *) printf 'no tracked shellcheck artifact for %s\n' "$(uname -s)" >&2; exit 1 ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch="x86_64" ;;
    aarch64 | arm64) arch="aarch64" ;;
    *) printf 'no tracked shellcheck artifact for %s\n' "$(uname -m)" >&2; exit 1 ;;
  esac
  platform="${os}.${arch}"
  digest=$(platform_digest "$platform") || {
    printf 'no tracked shellcheck digest for %s\n' "$platform" >&2
    exit 1
  }

  DOWNLOAD_HOME=$(mktemp -d "${TMPDIR:-/tmp}/gds-shellcheck.XXXXXX")
  curl -fsSL \
    "https://github.com/koalaman/shellcheck/releases/download/v${SHELLCHECK_VERSION}/shellcheck-v${SHELLCHECK_VERSION}.${platform}.tar.xz" \
    -o "$DOWNLOAD_HOME/shellcheck.tar.xz"
  verify_digest "$digest" "$DOWNLOAD_HOME/shellcheck.tar.xz"
  tar -xJf "$DOWNLOAD_HOME/shellcheck.tar.xz" --strip-components=1 \
    -C "$DOWNLOAD_HOME"
  SHELLCHECK_BIN="$DOWNLOAD_HOME/shellcheck"
  [ -x "$SHELLCHECK_BIN" ] || {
    printf 'shellcheck archive did not contain an executable\n' >&2
    exit 1
  }
}

missing=()
for script in "${SCRIPTS[@]}"; do
  [ -f "$ROOT/$script" ] || missing+=("$script")
done
if [ "${#missing[@]}" -ne 0 ]; then
  printf 'declared shell script is missing: %s\n' "${missing[@]}" >&2
  exit 1
fi

# An unlisted script is a silent hole in this gate, so refuse to pass while one
# exists rather than reporting a clean run over a subset.
untracked=$(
  git -C "$ROOT" ls-files -- 'scripts/*.sh' 'tools/*.sh' |
    grep -vxF -f <(printf '%s\n' "${SCRIPTS[@]}") || true
)
if [ -n "$untracked" ]; then
  printf 'shell script is tracked but not linted by this gate:\n%s\n' \
    "$untracked" >&2
  exit 1
fi

ensure_shellcheck

for script in "${SCRIPTS[@]}"; do
  bash -n "$ROOT/$script"
done
"$SHELLCHECK_BIN" --shell=bash --external-sources "${SCRIPTS[@]}"

printf 'GDS shell validation: PASS (%s scripts, shellcheck %s)\n' \
  "${#SCRIPTS[@]}" "$SHELLCHECK_VERSION"
