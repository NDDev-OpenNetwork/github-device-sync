"""Execute the hosted input step against the native bounded archive reader."""
from pathlib import Path
import base64
import hashlib
import io
import json
import os
import subprocess
import tarfile

import pytest
import yaml

ROOT = Path(__file__).resolve().parents[1]


def evidence_step() -> str:
    doc = yaml.safe_load((ROOT / ".github/workflows/release-bundle.yml").read_text())
    steps = doc["jobs"]["build"]["steps"]
    names = [step.get("name") for step in steps]
    name = "Materialize bounded signed harness evidence input"
    assert names.index(name) < names.index("Run release gates")
    return steps[names.index(name)]["run"]


@pytest.fixture(scope="module")
def native_builder(tmp_path_factory: pytest.TempPathFactory) -> Path:
    binary = tmp_path_factory.mktemp("release-evidence-builder") / "gds-release-builder"
    subprocess.run(["go", "build", "-trimpath", "-o", str(binary), "./core/cmd/gds-release-builder"],
                   cwd=ROOT, check=True, capture_output=True, text=True)
    return binary


def run_input(tmp_path: Path, native_builder: Path, pin: str, *, channel="stable",
              archive=True, policy=True, malformed_archive=False):
    records = ["manifest", "antigravity", "claude-code", "codex", "cursor", "grok-build", "opencode", "pi"]
    stream = io.BytesIO()
    with tarfile.open(fileobj=stream, mode="w:gz", format=tarfile.USTAR_FORMAT) as tar:
        for name in records:
            info = tarfile.TarInfo(name + ".json")
            info.size, info.mode = 3, 0o600
            tar.addfile(info, io.BytesIO(b"{}\n"))
    raw_archive = b"invalid archive" if malformed_archive else stream.getvalue()
    trust = b'{"schema_version":1}\n'
    output = tmp_path / "outputs"
    step = evidence_step()
    command = "go run ./core/cmd/gds-release-builder"
    assert step.count(command) == 1
    step = step.replace(command, '"$TEST_RELEASE_BUILDER"')
    result = subprocess.run(["bash", "-c", step], cwd=ROOT, capture_output=True, text=True,
                            env={**os.environ, "TEST_RELEASE_BUILDER": str(native_builder),
                                 "RUNNER_TEMP": str(tmp_path), "EVIDENCE_INPUT_ROOT": str(tmp_path / "input"),
                                 "GITHUB_OUTPUT": str(output), "CHANNEL": channel,
                                 "HARNESS_EVIDENCE_BUNDLE_BASE64": base64.b64encode(raw_archive).decode() if archive else "",
                                 "HARNESS_EVIDENCE_TRUST_POLICY_BASE64": base64.b64encode(trust).decode() if policy else "",
                                 "HARNESS_EVIDENCE_TRUST_POLICY_DIGEST": pin}, timeout=30)
    return result, output


@pytest.mark.parametrize("prefix", ["", "sha256:"])
def test_both_exact_sha256_notations_reach_native_materialization(tmp_path: Path, native_builder: Path, prefix: str):
    digest = hashlib.sha256(b'{"schema_version":1}\n').hexdigest()
    result, output = run_input(tmp_path, native_builder, prefix + digest)
    assert result.returncode == 0, result.stderr
    assert "--harness-evidence-directory" in output.read_text()
    assert len(list((tmp_path / "input/records").glob("*.json"))) == 8


@pytest.mark.parametrize("pin,message", [
    ("", "must be an independent SHA-256 pin"),
    ("sha512:" + "a" * 64, "must be an independent SHA-256 pin"),
    ("sha256:not-a-digest", "must be an independent SHA-256 pin"),
    ("sha256:" + "0" * 64, "does not match the independent repository pin"),
])
def test_missing_malformed_and_mismatched_independent_pins_fail(tmp_path: Path, native_builder: Path, pin: str, message: str):
    result, output = run_input(tmp_path, native_builder, pin)
    assert result.returncode != 0 and message in result.stderr
    assert not output.exists()
    assert not (tmp_path / "input/records").exists()


@pytest.mark.parametrize("archive,policy", [(True, False), (False, True), (False, False)])
def test_stable_requires_both_signed_inputs(tmp_path: Path, native_builder: Path, archive: bool, policy: bool):
    result, output = run_input(tmp_path, native_builder, "", archive=archive, policy=policy)
    assert result.returncode != 0
    assert not output.exists()


def test_canary_can_explicitly_omit_both_inputs(tmp_path: Path, native_builder: Path):
    result, output = run_input(tmp_path, native_builder, "", channel="canary", archive=False, policy=False)
    assert result.returncode == 0, result.stderr
    assert output.read_text() == "arguments=\n"


def test_native_archive_refusal_is_visible_in_the_step_log(tmp_path: Path, native_builder: Path):
    digest = hashlib.sha256(b'{"schema_version":1}\n').hexdigest()
    result, output = run_input(tmp_path, native_builder, digest, malformed_archive=True)
    assert result.returncode != 0
    assert json.loads(result.stderr)["code"] == "GDS_HARNESS_EVIDENCE_ARCHIVE_INVALID"
    assert not output.exists()
