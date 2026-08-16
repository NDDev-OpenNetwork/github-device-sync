from pathlib import Path
import os
import subprocess


ROOT = Path(__file__).resolve().parents[1]


def test_exact_apply_helper_is_argv_only_and_runs_separate_enablement() -> None:
    script = (ROOT / "scripts" / "gds-exact-apply.sh").read_text(encoding="utf-8")
    assert 'operation enable "$plan_id"' in script
    assert '--approval-file "$approval_file"' in script
    assert '"${command[@]}" --apply "$plan_id"' in script
    assert '"${command[@]}" --verify "$operation_id"' in script
    assert "eval " not in script
    assert 'approval file must have mode 0600' in script


def test_exact_apply_helper_executes_enable_apply_verify_in_order(tmp_path: Path) -> None:
    fake = tmp_path / "gds"
    log = tmp_path / "calls"
    fake.write_text(
        "#!/usr/bin/env bash\nset -eu\nprintf '%s\\n' \"$*\" >> \"$CALL_LOG\"\n"
        "case \" $* \" in\n"
        "  *' operation enable '*) printf '{\"result\":\"succeeded\",\"data\":{\"status\":\"active\"}}\\n' ;;\n"
        "  *' --apply '*) printf '{\"result\":\"succeeded\",\"operation_id\":\"op-fixture\"}\\n' ;;\n"
        "  *' --verify '*) printf '{\"result\":\"succeeded\"}\\n' ;;\n"
        "  *) exit 2 ;;\n"
        "esac\n",
        encoding="utf-8",
    )
    fake.chmod(0o755)
    approval = tmp_path / "approval.json"
    approval.write_text("{}\n", encoding="utf-8")
    approval.chmod(0o600)
    result = subprocess.run(
        [str(ROOT / "scripts" / "gds-exact-apply.sh"), "--state-path", str(tmp_path / "state.db"),
         "--device-id", "device:test", "--session-id", "session-test",
         "--plan-id", "plan_0123456789ABCDEF", "--approval-file", str(approval), "--",
         str(fake), "--json", "harness", "install", "--harness", "codex"],
        check=True, capture_output=True, text=True, env={**os.environ, "CALL_LOG": str(log)},
    )
    assert '"operation_id":"op-fixture"' in result.stdout
    calls = log.read_text(encoding="utf-8").splitlines()
    assert "operation enable" in calls[0]
    assert "--apply plan_0123456789ABCDEF" in calls[1]
    assert "--verify op-fixture" in calls[2]
