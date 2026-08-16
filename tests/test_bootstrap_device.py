"""Contracts for the source-built GDS bootstrap boundary."""

from __future__ import annotations

import hashlib
import json
import os
import re
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
BOOTSTRAP = ROOT / "scripts" / "bootstrap-device.sh"


def test_source_build_version_binds_release_and_commit() -> None:
    version = subprocess.run(
        [str(BOOTSTRAP), "--source-build-version"],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
    revision = subprocess.run(
        ["git", "rev-parse", "--short=12", "HEAD"],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()

    pattern = (
        r"[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?"
        r"\+source\.[0-9a-f]{12}(?:\.dirty)?"
    )
    assert re.fullmatch(pattern, version)
    assert f"+source.{revision}" in version


def test_source_build_never_accepts_a_merely_runnable_binary() -> None:
    script = BOOTSTRAP.read_text(encoding="utf-8")

    assert 'expected_output="gds version ${expected_version}"' in script
    assert '"$GDS_BIN_TARGET" --version 2>/dev/null' in script
    assert 'cli.Version=${expected_version}' in script
    assert "if ! source_build_dirty" in script


def test_registration_skip_binds_full_control_plane_locator() -> None:
    script = BOOTSTRAP.read_text(encoding="utf-8")

    assert "registered_repository" in script
    assert "registered_root" in script
    assert "registered_anchor" in script
    assert 'yaml_get "$ROOT/.gds/repository.yaml" "repository.id"' in script
    assert 'expected_root=$(cd "$ROOT" && pwd -P)' in script
    assert 'expected_anchor="sha256:$(sha256_file "$ROOT/.gds/repository.yaml")"' in script
    assert "sha256sum" in script
    assert "shasum -a 256" in script
    assert '[ "$registered_repository" = "$expected_repository" ]' in script
    assert '[ "$registered_root" = "$expected_root" ]' in script
    assert '[ "$registered_anchor" = "$expected_anchor" ]' in script


def test_phase_three_plans_stale_anchor_and_skips_exact_locator(
    tmp_path: Path,
) -> None:
    home = tmp_path / "home"
    binary = home / ".local" / "bin" / "gds"
    config = home / ".config" / "github-device-sync"
    log = tmp_path / "gds.log"
    fake_path = tmp_path / "bin"
    binary.parent.mkdir(parents=True)
    config.mkdir(parents=True)
    fake_path.mkdir()
    binary.write_text(
        """#!/usr/bin/env bash
set -eu
printf '%s\\n' "$*" >> "$FAKE_GDS_LOG"
case " $* " in
  *" workspace register-estate --plan "*) printf '{"plan":{"plan_id":"plan-1"}}\\n' ;;
  *" workspace register-estate --apply "*) printf '{"operation_id":"operation-1"}\\n' ;;
  *" harness sync "*) printf '{"data":{"sync_plan":{}}}\\n' ;;
  *) printf '{"result":"succeeded"}\\n' ;;
esac
""",
        encoding="utf-8",
    )
    binary.chmod(0o755)
    gh = fake_path / "gh"
    gh.write_text("#!/usr/bin/env bash\nexit 1\n", encoding="utf-8")
    gh.chmod(0o755)
    python = fake_path / "python3"
    python.write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
    python.chmod(0o755)

    device = ROOT / "estate" / "devices" / "example-user-ubuntu-1.yaml"
    device_id = "device_01JEXAMPZ00000000000000002"
    repository_id = "repo_01JEXAMPZ0000000000000000A"
    root = str(ROOT.resolve())
    anchor = "sha256:" + hashlib.sha256(
        (ROOT / ".gds" / "repository.yaml").read_bytes()
    ).hexdigest()
    registration = config / "estate-registration.json"
    registration.write_text(
        json.dumps(
            {
                "schema_version": 1,
                "device_id": device_id,
                "estate": {
                    "repository_id": repository_id,
                    "root": root,
                    "anchor_digest": "sha256:" + "0" * 64,
                },
            }
        ),
        encoding="utf-8",
    )
    environment = {
        **os.environ,
        "HOME": str(home),
        "XDG_CONFIG_HOME": str(home / ".config"),
        "FAKE_GDS_LOG": str(log),
        "PATH": str(fake_path) + os.pathsep + os.environ["PATH"],
    }
    first = subprocess.run(
        [
            str(BOOTSTRAP),
            "--device",
            str(device),
            "--phase",
            "3",
            "--plan",
        ],
        cwd=ROOT,
        env=environment,
        check=True,
        capture_output=True,
        text=True,
    )
    assert "anchor changed; refreshing" in first.stdout
    assert "PLAN: gds workspace register-estate --plan" in first.stdout

    registration.write_text(
        json.dumps(
            {
                "schema_version": 1,
                "device_id": device_id,
                "estate": {
                    "repository_id": repository_id,
                    "root": root,
                    "anchor_digest": anchor,
                },
            }
        ),
        encoding="utf-8",
    )
    log.unlink()
    second = subprocess.run(
        [
            str(BOOTSTRAP),
            "--device",
            str(device),
            "--phase",
            "3",
            "--plan",
        ],
        cwd=ROOT,
        env=environment,
        check=True,
        capture_output=True,
        text=True,
    )
    assert "already complete" in second.stdout
    assert "workspace register-estate" not in log.read_text(encoding="utf-8")


def test_phase_three_combined_apply_is_removed() -> None:
    device = ROOT / "estate" / "devices" / "example-user-ubuntu-1.yaml"
    result = subprocess.run(
        [str(BOOTSTRAP), "--device", str(device), "--phase", "3", "--apply"],
        cwd=ROOT, capture_output=True, text=True,
    )
    assert result.returncode != 0
    assert "exact per-plan approve, enable, apply, and verify" in result.stderr
