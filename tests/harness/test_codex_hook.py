from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
HOOK = ROOT / "plugins/gds-core/hooks/gds_hook.py"


class CodexHookTests(unittest.TestCase):
    def run_hook(
        self,
        mode: str,
        payload: dict[str, object],
        *,
        environment: dict[str, str] | None = None,
    ) -> dict[str, object]:
        result = subprocess.run(
            [sys.executable, str(HOOK), mode],
            input=json.dumps(payload),
            text=True,
            capture_output=True,
            check=False,
            timeout=5,
            env=environment,
        )
        self.assertEqual(0, result.returncode, result.stderr)
        value = json.loads(result.stdout)
        self.assertIsInstance(value, dict)
        return value

    def test_blocks_direct_force_push(self) -> None:
        result = self.run_hook(
            "pre-tool-use",
            {
                "cwd": str(ROOT),
                "tool_name": "Bash",
                "tool_input": {"command": "git push origin main --force"},
            },
        )
        output = result["hookSpecificOutput"]
        self.assertEqual("deny", output["permissionDecision"])

    def test_allows_non_destructive_git_status(self) -> None:
        result = self.run_hook(
            "pre-tool-use",
            {
                "cwd": str(ROOT),
                "tool_name": "Bash",
                "tool_input": {"command": "git status --short"},
            },
        )
        self.assertEqual({}, result)

    def test_missing_gds_is_reported_not_proven(self) -> None:
        environment = {**os.environ, "PATH": "", "GDS_BIN": ""}
        result = self.run_hook(
            "session-start",
            {"cwd": str(ROOT), "hook_event_name": "SessionStart"},
            environment=environment,
        )
        context = result["hookSpecificOutput"]["additionalContext"]
        self.assertIn("NOT_PROVEN", context)

    def test_stop_hook_accepts_successful_validation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fake_gds = Path(directory) / "gds"
            fake_gds.write_text(
                '#!/bin/sh\nprintf \'%s\\n\' \'{"result":"succeeded","findings":[]}\'\n',
                encoding="utf-8",
            )
            fake_gds.chmod(0o755)
            environment = {**os.environ, "GDS_BIN": str(fake_gds)}
            result = self.run_hook(
                "stop",
                {"cwd": str(ROOT), "hook_event_name": "Stop"},
                environment=environment,
            )
        self.assertEqual({"continue": True}, result)


if __name__ == "__main__":
    unittest.main()
