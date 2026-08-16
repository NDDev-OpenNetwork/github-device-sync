#!/usr/bin/env bash
# Execute one already-planned GDS transaction through the normative
# enable -> apply -> verify sequence. The approval must bind the exact plan.
set -euo pipefail

die() { printf 'gds-exact-apply: %s\n' "$*" >&2; exit 2; }

state_path=""
device_id=""
session_id=""
plan_id=""
approval_file=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --state-path) state_path="${2:?}"; shift 2 ;;
    --device-id) device_id="${2:?}"; shift 2 ;;
    --session-id) session_id="${2:?}"; shift 2 ;;
    --plan-id) plan_id="${2:?}"; shift 2 ;;
    --approval-file) approval_file="${2:?}"; shift 2 ;;
    --) shift; break ;;
    *) die "unknown argument $1" ;;
  esac
done

[ -n "$state_path" ] && [ -n "$device_id" ] && [ -n "$session_id" ] &&
  [ -n "$plan_id" ] && [ -n "$approval_file" ] || die "all identity and evidence arguments are required"
[[ "$plan_id" =~ ^plan_[A-Za-z0-9_-]{10,120}$ ]] || die "plan id is invalid"
[ "$#" -gt 0 ] || die "base GDS lifecycle command is required after --"
[ -f "$approval_file" ] && [ ! -L "$approval_file" ] || die "approval must be a regular non-symlink file"
[ "$(stat -c '%a' "$approval_file" 2>/dev/null || stat -f '%Lp' "$approval_file")" = 600 ] || die "approval file must have mode 0600"
command=("$@")
gds_bin="${command[0]}"
command -v jq >/dev/null 2>&1 || die "jq is required"

"$gds_bin" --json operation enable "$plan_id" \
  --state-path "$state_path" --approval-file "$approval_file" \
  --device-id "$device_id" --session-id "$session_id" |
  jq -e '.result == "succeeded" and .data.status == "active"' >/dev/null

apply_output=$("${command[@]}" --apply "$plan_id" --approval-ref "$approval_file" \
  --state-path "$state_path" --device-id "$device_id" --session-id "$session_id")
operation_id=$(printf '%s' "$apply_output" | jq -er 'select(.result == "succeeded") | .operation_id')

"${command[@]}" --verify "$operation_id" --state-path "$state_path" \
  --device-id "$device_id" --session-id "$session_id" |
  jq -e '.result == "succeeded"' >/dev/null

printf '%s\n' "$apply_output"
