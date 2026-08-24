#!/usr/bin/env bash
#
# scripts/bootstrap-device.sh
# ------------------------------------------------------------
# Phased GDS device bootstrap orchestrator.
#
# A device is brought up through three independent mutation boundaries,
# each approved separately. This orchestrator reads the device-class
# intent from a device descriptor (estate/devices/<device>.yaml) and
# drives the boundaries in order, deriving the macos-ubuntu-bootstrap
# OS-installer flags from the descriptor's `class:` block so the device
# intent and the OS installer it drives cannot disagree.
#
#   Phase 0  read-only preflight (no mutation)
#   Phase 1  seed the Go toolchain + build the gds CLI from source
#   Phase 2  OS bootstrap (modules/macos-ubuntu-bootstrap) — installs
#            dev tools, language hosts, AI CLIs, browser layer
#   Phase 3  read-only control-plane planning and diagnostics:
#             3a   release install        (release mode only)
#             3b   workspace register-estate
#             3b'  device-local gh CLI runtime config (from estate installations)
#             3c   harness sync classification
#             3d   gds doctor
#
# There is intentionally no `gds bootstrap` CLI verb. The control plane
# keeps plan/approval/apply/verify at each boundary; this script only
# sequences them. Nothing clones a mutable default branch and nothing
# edits ~/.bashrc silently — the PATH export is printed at the end.
#
# Modes:
#   --plan    (default) print what would happen; no mutation
#   --apply   perform installer phases 0-2 only
#
# Usage:
#   scripts/bootstrap-device.sh --device estate/devices/<device>.yaml --plan
#   scripts/bootstrap-device.sh --device estate/devices/<device>.yaml --phase 1 --apply
#
# Phase-3 mutations use explicit exact-plan commands and
# scripts/gds-exact-apply.sh; this orchestrator cannot combine approvals.
# ------------------------------------------------------------

set -euo pipefail

# ----------------------------- paths -----------------------------
SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)

# Pinned toolchain (the security floor enforced by validate_go_core.sh).
GO_VERSION="1.26.5"
SDK_ROOT="${HOME}/sdk"
GO_HOME="${SDK_ROOT}/go${GO_VERSION}"
GDS_BIN_TARGET="${HOME}/.local/bin/gds"

# Device-local private configuration roots (XDG defaults; never tracked).
GDS_CONFIG_DIR="${XDG_CONFIG_HOME:-${HOME}/.config}/github-device-sync"
GDS_RUNTIME_CONFIG="${GDS_CONFIG_DIR}/github-runtime.yaml"

# macos-ubuntu-bootstrap submodule.
BOOTSTRAP_SCRIPT="${ROOT}/modules/macos-ubuntu-bootstrap/scripts/bootstrap.sh"
DEVICE_INTEGRITY="${ROOT}/modules/macos-ubuntu-bootstrap/scripts/device_integrity.py"

# ----------------------------- helpers -----------------------------
info() { printf '\033[1;34m==> %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m  \u2713 %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m  ! %s\033[0m\n' "$*"; }
die()  { printf '\033[1;31m  \u2717 %s\033[0m\n' "$*" >&2; exit 1; }

# Minimal YAML field reader. Reads a device descriptor and echoes the
# value of a dotted path (device.id, device.class.profile, ...). It only
# supports the flat indentation a device descriptor uses; jq/yq are not a
# prerequisite for this orchestrator. Returns 1 if the path is absent.
yaml_get() {
  local file="$1" path="$2"
  awk -v path="$path" '
    function strip(s){ sub(/^[ \t]+/,"",s); sub(/[ \t]+$/,"",s); return s }
    BEGIN { depth=split(path,p,"."); for(i=1;i<=depth;i++) want[i]=p[i] }
    {
      line=$0; sub(/#.*/,"",line)
      if (line ~ /^[ \t]*$/) next
      match(line, /^[ \t]*/); indent=RLENGTH
      s=substr(line,indent+1)
      eq=index(s,":"); if(eq==0) next
      k=strip(substr(s,1,eq-1)); v=strip(substr(s,eq+1))
      gsub(/^"|"$/,"",v)
      # record (key, indent) path
      keys[ctr]=k; inds[ctr]=indent; vals[ctr]=v; ctr++
    }
    END {
      # find a chain matching want[] with strictly increasing indent
      for(a=0;a<ctr;a++){
        if(keys[a]!=want[1]) continue
        curind=inds[a]
        if(depth==1){ print vals[a]; found=1; break }
        for(b=a+1;b<ctr;b++){
          if(inds[b]<=curind) break
          if(keys[b]==want[2]){
            if(depth==2){ if(vals[b]!=""){print vals[b]; found=1} break }
            curind2=inds[b]
            for(c=b+1;c<ctr;c++){
              if(inds[c]<=curind2) break
              if(keys[c]==want[3]){
                if(depth==3){ if(vals[c]!=""){print vals[c]; found=1} break }
                curind3=inds[c]
                for(d=c+1;d<ctr;d++){
                  if(inds[d]<=curind3) break
                  if(keys[d]==want[4] && depth==4){ if(vals[d]!=""){print vals[d]; found=1} break }
                }
                break
              }
            }
          }
        }
        if(found) break
      }
    }
  ' "$file"
}

require_cmd() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not on PATH"; }

sha256_file() {
  local path="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$path" | awk '{print $1}'
  else
    die "sha256sum or shasum is required to verify the estate anchor"
  fi
}

source_build_dirty() {
  [ -n "$(git -C "$ROOT" status --porcelain --untracked-files=all -- core go.mod go.sum)" ]
}

source_build_version() {
  local tag base revision dirty_suffix=""
  tag=$(git -C "$ROOT" describe --tags --match 'gds-v[0-9]*' --abbrev=0 2>/dev/null || true)
  base=${tag#gds-v}
  [ -n "$base" ] || base="0.1.0-dev"
  revision=$(git -C "$ROOT" rev-parse --short=12 HEAD)
  source_build_dirty && dirty_suffix=".dirty"
  printf '%s+source.%s%s\n' "$base" "$revision" "$dirty_suffix"
}

# ----------------------------- args -----------------------------
DEVICE_PATH=""
APPLY=0
PHASE_ONLY=""
FROM_PHASE=""
PRINT_SOURCE_BUILD_VERSION=0

usage() {
  cat <<EOF
Usage: $(basename "$0") --device <path> [--plan|--apply] [options]

Required:
  --device <path>        device descriptor (estate/devices/<device>.yaml)

Mode (exactly one):
  --plan                 read-only: print the plan, mutate nothing (default)
  --apply                perform mutations, stopping for approval evidence

Apply-mode options:
  Phase 3 mutations are intentionally not combined here. Use the emitted exact
  per-operation plan, sign it with 'gds operation approve', then execute it with
  scripts/gds-exact-apply.sh. --apply is limited to installer phases 0-2.

Phase control (optional):
  --phase <N>            run only a single phase (0|1|2|3)
  --from-phase <N>       resume starting at phase N

Other:
  --source-build-version print the deterministic source-build version and exit
  -h, --help             show this help
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --device) DEVICE_PATH="${2:?--device requires a path}"; shift 2;;
    --apply) APPLY=1; shift;;
    --plan) APPLY=0; shift;;
    --approval-ref) die "--approval-ref is removed: one reference cannot authorize multiple exact plans";;
    --phase) PHASE_ONLY="${2:?--phase requires a number}"; shift 2;;
    --from-phase) FROM_PHASE="${2:?--from-phase requires a number}"; shift 2;;
    --source-build-version) PRINT_SOURCE_BUILD_VERSION=1; shift;;
    -h|--help) usage; exit 0;;
    *) die "unknown argument: $1";;
  esac
done

if [ "$PRINT_SOURCE_BUILD_VERSION" -eq 1 ]; then
  source_build_version
  exit 0
fi

[ -n "$DEVICE_PATH" ] || { usage >&2; exit 2; }
if [ "$APPLY" -eq 1 ]; then
  case "${PHASE_ONLY:-}:${FROM_PHASE:-}" in
    0:|1:|2:) ;;
    *) die "phase 3 combined apply is removed; use exact per-plan approve, enable, apply, and verify" ;;
  esac
fi

# Resolve a relative device path against the repo root.
[[ "$DEVICE_PATH" = /* ]] || DEVICE_PATH="$ROOT/$DEVICE_PATH"
[ -f "$DEVICE_PATH" ] || die "device descriptor not found: $DEVICE_PATH"

# Read the descriptor's load-bearing fields.
DEVICE_OS=$(yaml_get "$DEVICE_PATH" "device.os" || true)
DEVICE_ARCH=$(yaml_get "$DEVICE_PATH" "device.architecture" || true)
DEVICE_ID=$(yaml_get "$DEVICE_PATH" "device.id" || true)
DEVICE_NAME=$(yaml_get "$DEVICE_PATH" "device.name" || true)
CLASS_PROFILE=$(yaml_get "$DEVICE_PATH" "device.class.profile" || true)
CLASS_GUI=$(yaml_get "$DEVICE_PATH" "device.class.gui" || true)
CLASS_DOCKER=$(yaml_get "$DEVICE_PATH" "device.class.docker_mode" || true)

[ -n "$DEVICE_OS" ] || die "device.os missing in $DEVICE_PATH"
[ -n "$DEVICE_ARCH" ] || die "device.architecture missing in $DEVICE_PATH"
[ -n "$DEVICE_ID" ] || die "device.id missing in $DEVICE_PATH"

# Map the descriptor OS to the bootstrap platform name.
case "$DEVICE_OS" in
  macos) PLATFORM="macos";;
  linux) PLATFORM="ubuntu";;
  *) die "unsupported device.os '$DEVICE_OS' (expected macos|linux)";;
esac

# Derive the OS-installer profile and flags from the class block, mirroring
# modules/macos-ubuntu-bootstrap/scripts/bootstrap.sh rules. If no class is
# declared, default to desktop so the orchestrator never infers a server.
PROFILE="${CLASS_PROFILE:-desktop}"
OS_ARGS=("--platform" "$PLATFORM" "--profile" "$PROFILE")
if [ "$PROFILE" = "desktop" ]; then
  case "${CLASS_GUI:-enabled}" in
    enabled) OS_ARGS+=("--gui");;
    disabled) OS_ARGS+=("--no-gui");;
  esac
fi
if [ "$PROFILE" = "server" ]; then
  # server is always headless; docker_mode defaults to rootful per contract.
  OS_ARGS+=("--no-gui" "--docker-mode" "${CLASS_DOCKER:-rootful}")
  # Server-only hardening toggles, if declared.
  HARDEN_SSH=$(yaml_get "$DEVICE_PATH" "device.class.hardening.ssh" || true)
  HARDEN_UFW=$(yaml_get "$DEVICE_PATH" "device.class.hardening.ufw" || true)
  HARDEN_F2B=$(yaml_get "$DEVICE_PATH" "device.class.hardening.fail2ban" || true)
  [ "${HARDEN_SSH:-}" = "true" ] && OS_ARGS+=("--harden-ssh")
  [ "${HARDEN_UFW:-}" = "true" ] && OS_ARGS+=("--enable-ufw")
  [ "${HARDEN_F2B:-}" = "true" ] && OS_ARGS+=("--with-fail2ban")
fi

run_phase() {
  local n="$1"
  if [ -n "$PHASE_ONLY" ] && [ "$n" != "$PHASE_ONLY" ]; then return 0; fi
  if [ -n "$FROM_PHASE" ] && [ "$n" -lt "$FROM_PHASE" ]; then return 0; fi
  "phase_$n"
}

# ----------------------------- phase 0 -----------------------------
phase_0() {
  info "Phase 0 — preflight (read-only)"
  require_cmd git
  [ "$PLATFORM" = "ubuntu" ] && require_cmd wget
  # Detect the host; warn (do not die) if the descriptor disagrees with the
  # host OS/arch — the operator may be planning for another machine.
  local host_os host_arch
  case "$(uname -s)" in
    Darwin) host_os="macos";;
    Linux) host_os="linux";;
    *) host_os="$(uname -s)";;
  esac
  case "$(uname -m)" in
    x86_64|amd64) host_arch="x86_64";;
    aarch64|arm64) host_arch="arm64";;
    *) host_arch="$(uname -m)";;
  esac
  [ "$host_os" = "$DEVICE_OS" ] || warn "descriptor os=$DEVICE_OS but host os=$host_os"
  [ "$host_arch" = "$DEVICE_ARCH" ] || warn "descriptor architecture=$DEVICE_ARCH but host architecture=$host_arch"
  ok "device: $DEVICE_NAME ($DEVICE_ID)  os=$DEVICE_OS arch=$DEVICE_ARCH class=$PROFILE"
  ok "control-plane root: $ROOT"
  [ -f "$BOOTSTRAP_SCRIPT" ] || die "macos-ubuntu-bootstrap submodule not initialized: $BOOTSTRAP_SCRIPT"
  ok "OS installer present: modules/macos-ubuntu-bootstrap"
  if gh auth status >/dev/null 2>&1; then ok "gh authenticated"; else warn "gh not authenticated (needed for estate operations)"; fi
  # Device integrity receipt (read-only). If a receipt exists, report whether
  # the device matches it and the contract before any mutation. A missing
  # receipt is expected on a fresh device — it is built during the first apply.
  if [ -f "$DEVICE_INTEGRITY" ]; then
    if python3 "$DEVICE_INTEGRITY" verify --json >/dev/null 2>&1; then
      ok "device integrity: PROVEN (matches receipt + contract)"
    else
      warn "device integrity: NOT_PROVEN — run 'python3 $DEVICE_INTEGRITY verify' for detail"
    fi
  fi
  info "OS-installer derived flags: ${OS_ARGS[*]}"
  info "Mode: $([ "$APPLY" -eq 1 ] && echo APPLY || echo PLAN)"
  if [ "$APPLY" -eq 0 ]; then
    info "PLAN: phases 1 (seed Go + build gds), 2 (OS bootstrap), 3 (control-plane staged). Re-run with --apply to execute."
  fi
}

# ensure_c_compiler guarantees a C compiler is present so Go can build cgo and
# run the -race assurance gate. On Ubuntu this is build-essential (gcc/g++);
# on macOS the Xcode Command Line Tools provide cc. The OS bootstrap
# (macos-ubuntu-bootstrap) does not install it, so the control-plane boundary
# acquires it here. This step needs sudo; it is skipped (warned) without it.
ensure_c_compiler() {
  if command -v gcc >/dev/null 2>&1 || command -v cc >/dev/null 2>&1; then
    ok "C compiler present ($(command -v gcc || command -v cc))"
    return 0
  fi
  case "$PLATFORM" in
    ubuntu)
      [ "$APPLY" -eq 1 ] || { info "PLAN: sudo apt-get install build-essential"; return 0; }
      if sudo -n true 2>/dev/null; then
        sudo apt-get update -qq && sudo apt-get install -y --no-install-recommends build-essential
        ok "build-essential installed (C compiler for CGO/race gate)"
      else
        warn "no passwordless sudo; C compiler (build-essential) not installed."
        warn "run: sudo apt-get install -y build-essential  (enables validate_assurance.sh -race)"
      fi
      ;;
    macos)
      [ "$APPLY" -eq 1 ] || { info "PLAN: xcode-select --install (Command Line Tools)"; return 0; }
      if xcode-select -p >/dev/null 2>&1; then
        ok "Xcode Command Line Tools present"
      else
        warn "run: xcode-select --install  (provides cc for CGO/race gate)"
      fi
      ;;
  esac
}

# ----------------------------- phase 1 -----------------------------
phase_1() {
  info "Phase 1 — seed Go ${GO_VERSION} + build gds from source"
  ensure_c_compiler
  local go_bin="${GO_HOME}/bin/go"
  if [ -x "$go_bin" ] && GOTOOLCHAIN=local "$go_bin" version 2>/dev/null | grep -q "$GO_VERSION"; then
    ok "Go ${GO_VERSION} already installed at ${GO_HOME}"
  else
    [ "$APPLY" -eq 1 ] || { info "PLAN: install Go ${GO_VERSION} to ${GO_HOME}"; return 0; }
    local goos goarch tarball
    goos=$(uname -s | tr '[:upper:]' '[:lower:]')
    goarch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
    tarball="go${GO_VERSION}.${goos}-${goarch}.tar.gz"
    local url="https://go.dev/dl/${tarball}"
    info "Downloading ${url}"
    local tmp; tmp=$(mktemp)
    wget -nv -O "$tmp" "$url"
    rm -rf "$GO_HOME"
    mkdir -p "$GO_HOME"
    tar -xzf "$tmp" -C "$GO_HOME" --strip-components=1
    rm -f "$tmp"
    GOTOOLCHAIN=local "$go_bin" version
    ok "Go ${GO_VERSION} installed"
  fi

  export GOTOOLCHAIN="go${GO_VERSION}"
  export PATH="$GO_HOME/bin:${HOME}/go/bin:${PATH}"

  local expected_version expected_output
  expected_version=$(source_build_version)
  expected_output="gds version ${expected_version}"
  if ! source_build_dirty && [ -x "$GDS_BIN_TARGET" ] &&
    [ "$("$GDS_BIN_TARGET" --version 2>/dev/null)" = "$expected_output" ]; then
    ok "gds source build is current at ${GDS_BIN_TARGET} (${expected_version})"
  else
    [ "$APPLY" -eq 1 ] || {
      info "PLAN: rebuild ${GDS_BIN_TARGET} from source as ${expected_version}"
      return 0
    }
    mkdir -p "$(dirname "$GDS_BIN_TARGET")"
    local candidate
    candidate=$(mktemp "${GDS_BIN_TARGET}.tmp.XXXXXX")
    if ! (cd "$ROOT" && go build -trimpath \
      -ldflags "-X github.com/NDDev-OpenNetwork/github-device-sync/core/cli.Version=${expected_version}" \
      -o "$candidate" ./core/cmd/gds); then
      rm -f -- "$candidate"
      die "gds source build failed"
    fi
    mv -f -- "$candidate" "$GDS_BIN_TARGET"
    ok "gds built: $("$GDS_BIN_TARGET" --version)"
  fi
}

gds() { "$GDS_BIN_TARGET" "$@"; }

# ----------------------------- phase 2 -----------------------------
phase_2() {
  info "Phase 2 — OS bootstrap (macos-ubuntu-bootstrap, profile=${PROFILE})"
  local args=("${OS_ARGS[@]}")
  if [ "$APPLY" -eq 1 ]; then
    args+=("--apply")
  else
    args+=("--plan")
  fi
  info "bash ${BOOTSTRAP_SCRIPT} ${args[*]}"
  [ "$APPLY" -eq 1 ] || { info "PLAN: OS bootstrap dry-run only (re-run with --apply)"; return 0; }
  bash "$BOOTSTRAP_SCRIPT" "${args[@]}"
  ok "OS bootstrap applied"
}

# ----------------------------- phase 3 -----------------------------
phase_3() {
  info "Phase 3 — control-plane staged commands"
  if [ ! -x "$GDS_BIN_TARGET" ]; then
    [ "$APPLY" -eq 0 ] || die "gds not built; run phase 1 first"
    info "PLAN: phase 1 builds gds first; run --apply to execute phases 1-3"
    return 0
  fi
  local session_id
  session_id="device-bootstrap-$(date -u +%Y%m%dT%H%M%SZ)"
  # The release-install boundary requires a packaged release. On a device
  # bootstrapped from source there is no release directory, so that boundary
  # is skipped in source mode and the freshly built gds is used directly.
  if [ -n "${RELEASE_DIRECTORY:-}" ] && [ -d "${RELEASE_DIRECTORY:-}" ]; then
    run_release_install "$session_id"
  else
    warn "no RELEASE_DIRECTORY set; skipping 3a release install (using source-built gds)"
  fi
  run_register_estate "$session_id"
  run_github_runtime "$session_id"
  run_harness_sync "$session_id"
  run_doctor
}

run_release_install() {
  local session_id="$1"
  info "Phase 3a — release install (plan/apply/verify)"
  local common=(--release-directory "${RELEASE_DIRECTORY:?}" \
                --evidence-directory "${EVIDENCE_DIRECTORY:?}" \
                --trust-policy "${LOCAL_TRUST_POLICY:?}" \
                --install-root "${GDS_INSTALL_ROOT:?}" \
                --state-path "${GDS_STATE_PATH:?}" \
                --device-id "$DEVICE_ID" --session-id "$session_id")
  info "PLAN: gds release install --plan ${common[*]}"
  info "Then sign the exact plan and execute it with scripts/gds-exact-apply.sh."
}

run_register_estate() {
  local session_id="$1"
  info "Phase 3b — workspace register-estate (plan/apply/verify)"
  local state_path="${GDS_STATE_PATH:-}"
  local reg_path="${GDS_REGISTRATION_PATH:-${GDS_CONFIG_DIR}/estate-registration.json}"
  # Idempotent only when the complete locator still binds this exact control
  # plane anchor. Device identity alone is insufficient: a legitimate anchor
  # change must refresh the registration or every non-control-plane context
  # fails closed with GDS_CONTEXT_ESTATE_NOT_REGISTERED.
  if [ -f "$reg_path" ]; then
    local registered_device registered_repository registered_root registered_anchor
    local expected_repository expected_root expected_anchor
    registered_device=$(jq -r '.device_id // empty' "$reg_path" 2>/dev/null || true)
    registered_repository=$(jq -r '.estate.repository_id // empty' "$reg_path" 2>/dev/null || true)
    registered_root=$(jq -r '.estate.root // empty' "$reg_path" 2>/dev/null || true)
    registered_anchor=$(jq -r '.estate.anchor_digest // empty' "$reg_path" 2>/dev/null || true)
    expected_repository=$(yaml_get "$ROOT/.gds/repository.yaml" "repository.id" || true)
    expected_root=$(cd "$ROOT" && pwd -P)
    expected_anchor="sha256:$(sha256_file "$ROOT/.gds/repository.yaml")"
    if [ "$registered_device" = "$DEVICE_ID" ] &&
      [ "$registered_repository" = "$expected_repository" ] &&
      [ "$registered_root" = "$expected_root" ] &&
      [ "$registered_anchor" = "$expected_anchor" ]; then
      ok "register-estate already complete for $DEVICE_ID; skipping"
      return 0
    fi
    info "estate registration identity or anchor changed; refreshing through plan/apply/verify"
  fi
  local plan_args=(--plan --estate-root "$ROOT")
  [ -n "$state_path" ] && plan_args+=(--state-path "$state_path")
  # Pass the registration path only when the operator chose one. `reg_path`
  # already resolved the default above, so the override is what is interesting
  # here — and it must be read through the same guarded expansion, because this
  # script runs under `set -u` and the variable is optional by construction.
  [ -n "${GDS_REGISTRATION_PATH:-}" ] && plan_args+=(--registration-path "$reg_path")
  plan_args+=(--device-id "$DEVICE_ID" --session-id "$session_id")
  info "PLAN: gds workspace register-estate ${plan_args[*]}"
  info "Then sign the exact plan and execute it with scripts/gds-exact-apply.sh."
}

run_github_runtime() {
  # Phase 3b' — write the device-local gh CLI runtime config.
  #
  # The gh CLI is the device's GitHub credential (ADR 0034). This step derives a
  # private 0600 github-runtime.yaml from the estate installations so a freshly
  # bootstrapped device can observe and reconcile its estate through gh without
  # a GitHub App private key. Each estate installation binds to its declared
  # account (login + type); the runtime references exactly mirror the estate
  # secret_ref set. Nothing here is a tracked estate source or a secret value.
  local session_id="$1"
  info "Phase 3b-prime — device-local gh CLI runtime config"
  gh auth status >/dev/null 2>&1 || { warn "gh not authenticated; skipping runtime config (run 'gh auth login' first)"; return 0; }
  local gh_account; gh_account=$(gh api user -q .login 2>/dev/null || true)
  [ -n "$gh_account" ] || { warn "gh token unreadable; skipping runtime config"; return 0; }
  local estate_install_dir="$ROOT/estate/installations"
  [ -d "$estate_install_dir" ] || { warn "no estate/installations directory; skipping runtime config"; return 0; }
  local entries=() refs=()
  for inst in "$estate_install_dir"/*.yaml; do
    [ -f "$inst" ] || continue
    local id account_login account_type secret_ref
    id=$(yaml_get "$inst" "installation.id" || true)
    account_login=$(yaml_get "$inst" "installation.account_login" || true)
    account_type=$(yaml_get "$inst" "installation.account_type" || true)
    # secret_ref is a quoted depth-3 field (installation.credentials.secret_ref).
    # yaml_get resolves it, but the value can carry ':' and quotes, which the
    # awk splitter strips; sed reads the exact quoted literal instead.
    secret_ref=$(sed -n 's/.*secret_ref: *"\([^"]*\)".*/\1/p' "$inst" | head -1 || true)
    [ -n "$id" ] && [ -n "$account_login" ] && [ -n "$account_type" ] && [ -n "$secret_ref" ] || continue
    entries+=("    \"$id\":\n      account_login: \"$account_login\"\n      account_type: \"$account_type\"")
    refs+=("    \"$secret_ref\": \"$account_login\"")
  done
  if [ "${#entries[@]}" -eq 0 ]; then
    warn "no estate installations parsed; skipping runtime config"; return 0
  fi
  if [ "$APPLY" -eq 0 ]; then
    info "PLAN: write gh CLI runtime config to ${GDS_RUNTIME_CONFIG} (${#entries[@]} installation(s))"
    return 0
  fi
  mkdir -p "$GDS_CONFIG_DIR"
  {
    printf 'schema_version: 1\n\n'
    printf 'github:\n  installations:\n'
    printf '%b\n' "${entries[@]}"
    printf '  max_repositories: 2000\n\n'
    printf 'secret_store:\n  provider: \"gh-cli\"\n  references:\n'
    printf '%b\n' "${refs[@]}"
  } > "$GDS_RUNTIME_CONFIG"
  chmod 600 "$GDS_RUNTIME_CONFIG"
  ok "gh CLI runtime config written: $GDS_RUNTIME_CONFIG (0600)"
  # Prove the binding: one inventory read per declared installation.
  local inst_id
  for inst in "$estate_install_dir"/*.yaml; do
    [ -f "$inst" ] || continue
    inst_id=$(yaml_get "$inst" "installation.id" || true)
    [ -n "$inst_id" ] || continue
    if gds --json --cwd "$ROOT" github inventory \
         --installation "$inst_id" --runtime-config "$GDS_RUNTIME_CONFIG" \
         2>/dev/null | grep -q '"result": *"succeeded"'; then
      ok "inventory proven for $inst_id"
    else
      warn "inventory not proven for $inst_id (check gh scopes / account access)"
    fi
  done
}

run_harness_sync() {
  local session_id="$1"
  info "Phase 3c — harness sync (classify then converge)"
  local target_root="${HARNESS_TARGET_ROOT:-$ROOT}"
  local classify_args=(--device "$DEVICE_PATH" --target-root "$target_root" --skill-profile core --scope project)
  gds --json harness sync "${classify_args[@]}" | jq '.data.sync_plan' >/dev/null 2>&1 || warn "harness sync classify returned non-zero (may be nothing to do)"
  [ "$APPLY" -eq 1 ] || { info "PLAN: run the explicit per-harness lifecycle action reported by gds harness sync"; return 0; }
  die "combined harness convergence is removed; use explicit per-harness lifecycle plans"
}

run_doctor() {
  info "Phase 3d — gds doctor"
  gds --json --cwd "$ROOT" doctor >/dev/null 2>&1 || warn "doctor reported findings"
  ok "doctor complete"
  # Rebuild and verify the device integrity receipt so the device is bound to
  # the contract it was just bootstrapped against. Build is only meaningful
  # after apply — plan leaves the existing receipt untouched.
  if [ "$APPLY" -eq 1 ] && [ -f "$DEVICE_INTEGRITY" ]; then
    # Pass the resolved class profile so the receipt records it: a server device
    # legitimately omits the desktop-only compiled hosts / pinned tools, and
    # without the profile they read as drift and force a permanent NOT_PROVEN.
    if python3 "$DEVICE_INTEGRITY" build --profile "$PROFILE" >/dev/null 2>&1; then
      if python3 "$DEVICE_INTEGRITY" verify --json >/dev/null 2>&1; then
        ok "device integrity receipt rebuilt and PROVEN"
      else
        warn "device integrity receipt rebuilt but verification failed — run 'python3 $DEVICE_INTEGRITY verify' for detail"
      fi
    else
      warn "device integrity receipt could not be built — run 'python3 $DEVICE_INTEGRITY build' for detail"
    fi
  fi
}

# ----------------------------- run -----------------------------
info "GDS device bootstrap — $([ "$APPLY" -eq 1 ] && echo APPLY || echo PLAN)"
run_phase 0
run_phase 1
run_phase 2
run_phase 3

if [ "$APPLY" -eq 1 ]; then
  ok "bootstrap complete"
else
  info "For phase 3, approve each exact emitted plan and use scripts/gds-exact-apply.sh."
fi
cat <<EOF

  Add the Go toolchain to your PATH (not done silently):
    export PATH="${GO_HOME}/bin:\${HOME}/go/bin:\$PATH"
EOF
