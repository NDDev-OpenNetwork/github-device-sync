#!/usr/bin/env python3
"""Small Codex hook adapter for the GDS core plugin.

The hook adds compact verified context and blocks a narrow set of obvious
direct destructive commands. It is deliberately not an authorization or
security boundary; GDS CLI policy and plan/apply/verify remain authoritative.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import Any


MAX_INPUT_BYTES = 1 << 20
MAX_CONTEXT_CHARS = 2400
GDS_TIMEOUT_SECONDS = 4

BLOCKED_COMMANDS: tuple[tuple[re.Pattern[str], str], ...] = (
    (
        re.compile(
            r"(?:^|[;&|]\s*)git\s+push\b[^\n;]*(?:--force(?:-with-lease)?|-f(?:\s|$))"
        ),
        "Direct force-push is blocked; use an explicit GDS plan with exact OID preconditions.",
    ),
    (
        re.compile(r"(?:^|[;&|]\s*)git\s+reset\s+--hard(?:\s|$)"),
        "Direct hard reset is blocked; preserve unrelated work and use a recovery plan.",
    ),
    (
        re.compile(r"(?:^|[;&|]\s*)git\s+clean\b[^\n;]*-[a-zA-Z]*f"),
        "Direct git clean is blocked; cleanup requires an exact verified target plan.",
    ),
    (
        re.compile(r"(?:^|[;&|]\s*)git\s+branch\s+-D(?:\s|$)"),
        "Forced branch deletion is blocked; prove reachability and cleanup eligibility first.",
    ),
    (
        re.compile(r"(?:^|[;&|]\s*)git\s+worktree\s+remove\b"),
        "Direct worktree removal is blocked; use an exact GDS cleanup plan.",
    ),
    (
        re.compile(r"(?:^|[;&|]\s*)gh\s+repo\s+delete\b"),
        "Direct repository deletion is blocked; deletion requires its dedicated GDS workflow.",
    ),
    (
        re.compile(r"(?:^|[;&|]\s*)gh\s+pr\s+merge\b"),
        "Direct pull-request merge is blocked; use the explicit complete-work plan.",
    ),
)


def read_payload() -> dict[str, Any]:
    raw = sys.stdin.buffer.read(MAX_INPUT_BYTES + 1)
    if len(raw) > MAX_INPUT_BYTES:
        raise ValueError("hook input exceeds the 1 MiB contract")
    value = json.loads(raw or b"{}")
    if not isinstance(value, dict):
        raise ValueError("hook input must be a JSON object")
    return value


def write_payload(value: dict[str, Any]) -> None:
    json.dump(value, sys.stdout, separators=(",", ":"), ensure_ascii=True)
    sys.stdout.write("\n")


def gds_binary() -> str | None:
    configured = os.environ.get("GDS_BIN")
    if not configured:
        return None
    path = Path(configured).expanduser()
    if not path.is_absolute() or path.is_symlink():
        return None
    if path.is_file() and os.access(path, os.X_OK):
        return str(path)
    return None


def command_environment() -> dict[str, str]:
    allowed = {
        "HOME",
        "LANG",
        "LC_ALL",
        "PATH",
        "TMPDIR",
        "XDG_CACHE_HOME",
        "XDG_CONFIG_HOME",
        "XDG_DATA_HOME",
        "XDG_STATE_HOME",
        "GDS_ESTATE_ROOT",
    }
    environment = {key: value for key, value in os.environ.items() if key in allowed}
    environment["GIT_TERMINAL_PROMPT"] = "0"
    return environment


def run_gds(cwd: str, *arguments: str) -> tuple[int, dict[str, Any] | None]:
    binary = gds_binary()
    if binary is None:
        return 127, None
    try:
        result = subprocess.run(
            [binary, "--cwd", cwd, "--timeout", "3s", "--json", *arguments],
            check=False,
            capture_output=True,
            text=True,
            timeout=GDS_TIMEOUT_SECONDS,
            env=command_environment(),
        )
    except (OSError, subprocess.TimeoutExpired):
        return 126, None
    try:
        payload = json.loads(result.stdout)
    except (json.JSONDecodeError, TypeError):
        payload = None
    return result.returncode, payload if isinstance(payload, dict) else None


def session_start(payload: dict[str, Any]) -> None:
    cwd = str(payload.get("cwd") or ".")
    return_code, result = run_gds(cwd, "context")
    if result is None:
        context = "GDS context is NOT_PROVEN because the pinned gds CLI was unavailable or returned invalid output."
    else:
        data = result.get("data") if isinstance(result.get("data"), dict) else {}
        repository = (
            data.get("repository") if isinstance(data.get("repository"), dict) else {}
        )
        mode = data.get("mode") if isinstance(data.get("mode"), dict) else {}
        policy = data.get("policy") if isinstance(data.get("policy"), dict) else {}
        context_data = (
            data.get("context") if isinstance(data.get("context"), dict) else {}
        )
        compact = {
            "result": result.get("result"),
            "repository_id": repository.get("id"),
            "roles": repository.get("roles"),
            "mode": mode.get("kind"),
            "bundle_version": policy.get("bundle_version"),
            "policy_digest": policy.get("digest"),
            "skill_profiles": context_data.get("skill_profiles"),
            "exit_code": return_code,
        }
        context = "Verified GDS session context: " + json.dumps(
            compact, separators=(",", ":"), ensure_ascii=True
        )
    write_payload(
        {
            "hookSpecificOutput": {
                "hookEventName": "SessionStart",
                "additionalContext": context[:MAX_CONTEXT_CHARS],
            }
        }
    )


def pre_tool_use(payload: dict[str, Any]) -> None:
    tool_input = payload.get("tool_input")
    command = tool_input.get("command") if isinstance(tool_input, dict) else None
    if not isinstance(command, str):
        write_payload({})
        return
    for pattern, reason in BLOCKED_COMMANDS:
        if pattern.search(command):
            write_payload(
                {
                    "hookSpecificOutput": {
                        "hookEventName": "PreToolUse",
                        "permissionDecision": "deny",
                        "permissionDecisionReason": reason,
                    }
                }
            )
            return
    write_payload({})


def stop(payload: dict[str, Any]) -> None:
    cwd = str(payload.get("cwd") or ".")
    return_code, result = run_gds(cwd, "validate", "repository")
    if return_code == 0 and result is not None:
        write_payload({"continue": True})
        return
    if result is None:
        message = "GDS stop validation is NOT_PROVEN because the gds CLI was unavailable or returned invalid output."
    else:
        codes = [
            finding.get("code")
            for finding in result.get("findings", [])
            if isinstance(finding, dict) and isinstance(finding.get("code"), str)
        ]
        message = "GDS stop validation did not pass: " + ", ".join(codes[:12])
    write_payload({"continue": True, "systemMessage": message[:MAX_CONTEXT_CHARS]})


def main() -> int:
    if len(sys.argv) != 2:
        return 64
    try:
        payload = read_payload()
        if sys.argv[1] == "session-start":
            session_start(payload)
        elif sys.argv[1] == "pre-tool-use":
            pre_tool_use(payload)
        elif sys.argv[1] == "stop":
            stop(payload)
        else:
            return 64
    except (ValueError, json.JSONDecodeError) as error:
        write_payload({"systemMessage": f"GDS hook input error: {error}"})
        return 0
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
