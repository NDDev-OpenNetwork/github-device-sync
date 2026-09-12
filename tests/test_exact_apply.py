from pathlib import Path
import json
import os
import subprocess

import pytest


ROOT = Path(__file__).resolve().parents[1]


@pytest.fixture(scope="module")
def native_gds(tmp_path_factory: pytest.TempPathFactory) -> Path:
    binary = tmp_path_factory.mktemp("exact-apply-cli") / "gds"
    subprocess.run(
        ["go", "build", "-trimpath", "-o", str(binary), "./core/cmd/gds"],
        cwd=ROOT, check=True, capture_output=True, text=True,
    )
    return binary


def run_helper(tmp_path: Path, arguments: list[str], native: Path | None = None,
               failure: str = "") -> tuple[subprocess.CompletedProcess[str], list[list[str]]]:
    fake = tmp_path / "gds shim"
    log = tmp_path / "calls.jsonl"
    fake.write_text(
        "#!/usr/bin/env python3\n"
        "import json, os, subprocess, sys\n"
        "args = sys.argv[1:]\n"
        "with open(os.environ['CALL_LOG'], 'a') as log:\n"
        "    log.write(json.dumps(args) + '\\n')\n"
        "phase = 'enable' if 'enable' in args else 'apply' if '--apply' in args else 'verify'\n"
        "if os.environ.get('FAIL_PHASE') == phase:\n"
        "    print(json.dumps({'result': 'failed'})); sys.exit(13)\n"
        "if phase == 'verify' and os.environ.get('NATIVE_GDS'):\n"
        "    result = subprocess.run([os.environ['NATIVE_GDS'], *args], capture_output=True, text=True)\n"
        "    with open(os.environ['VERIFY_OUTPUT'], 'w') as output:\n"
        "        output.write(result.stdout)\n"
        "    print(result.stdout, end=''); sys.stderr.write(result.stderr); sys.exit(result.returncode)\n"
        "print(json.dumps({'result': 'succeeded', 'data': {'status': 'active'}, 'operation_id': 'op-fixture'}))\n",
        encoding="utf-8",
    )
    fake.chmod(0o755)
    approval = tmp_path / "approval with spaces.json"
    approval.write_text("{}\n", encoding="utf-8")
    approval.chmod(0o600)
    result = subprocess.run(
        [str(ROOT / "scripts" / "gds-exact-apply.sh"), "--state-path", str(tmp_path / "state.db"),
         "--device-id", "device_01JAZZQ0000000000000000000", "--session-id", "session-test",
         "--plan-id", "plan_0123456789ABCDEF", "--approval-file", str(approval), "--",
         str(fake), "--json", *arguments],
        capture_output=True, text=True,
        env={**os.environ, "CALL_LOG": str(log), "FAIL_PHASE": failure,
             "NATIVE_GDS": str(native) if native else "", "VERIFY_OUTPUT": str(tmp_path / "verify.json")},
    )
    calls = [json.loads(line) for line in log.read_text(encoding="utf-8").splitlines()]
    return result, calls


def test_harness_selectors_and_literal_arguments_survive_verification(tmp_path: Path) -> None:
    target = str(tmp_path / "target with spaces $(must-not-execute) ; `literal`")
    base = ["harness", "install", "--harness", "codex", "--target-root", target]
    result, calls = run_helper(tmp_path, base)
    assert result.returncode == 0, result.stderr
    assert json.loads(result.stdout)["operation_id"] == "op-fixture"
    assert calls[0][:3] == ["--json", "operation", "enable"]
    assert calls[1][1:1 + len(base)] == calls[2][1:1 + len(base)] == base
    assert "--apply" in calls[1] and "--verify" in calls[2]
    assert "--approval-ref" not in calls[2]


@pytest.mark.parametrize("operation", ["install", "upgrade", "rollback", "remove"])
@pytest.mark.parametrize("equals", [False, True])
def test_release_verification_uses_stored_identity_with_native_cli(
    tmp_path: Path, native_gds: Path, operation: str, equals: bool,
) -> None:
    inputs = {"install-root": str(tmp_path / "install root")}
    if operation in {"install", "upgrade"}:
        inputs.update({name: str(tmp_path / f"{name} with spaces") for name in
                       ["release-directory", "evidence-directory", "trust-policy"]})
    if operation == "rollback":
        inputs.update({"target-release-key": "exact-key", "rollback-authorization": str(tmp_path / "rollback.json")})
    flags = [arg for name, value in inputs.items()
             for arg in ([f"--{name}={value}"] if equals else [f"--{name}", value])]
    result, calls = run_helper(tmp_path, ["release", operation, *flags], native_gds)
    assert result.returncode != 0  # The shim's synthetic operation was never stored.
    assert calls[1][3:3 + len(flags)] == flags
    assert calls[2][1:3] == ["release", operation]
    assert all(arg.split("=", 1)[0] not in {f"--{name}" for name in inputs} for arg in calls[2])
    envelope = json.loads((tmp_path / "verify.json").read_text(encoding="utf-8"))
    assert envelope["result"] != "succeeded"
    assert "GDS_RELEASE_VERIFY_INPUT_CONFLICT" not in json.dumps(envelope)
    # Native verification reached the operation store, beyond CLI input checks.
    assert envelope["findings"][0]["code"] == "GDS_LOCAL_OPERATION_NOT_PROVEN"
    assert envelope["findings"][0]["evidence"]["path"] == str(tmp_path / "state.db")


@pytest.mark.parametrize("phase,count", [("enable", 1), ("apply", 2), ("verify", 3)])
def test_failed_phase_cannot_report_success_or_start_next_phase(tmp_path: Path, phase: str, count: int) -> None:
    result, calls = run_helper(tmp_path, ["harness", "install", "--harness", "codex"], failure=phase)
    assert result.returncode != 0
    assert len(calls) == count
    assert '"operation_id"' not in result.stdout
