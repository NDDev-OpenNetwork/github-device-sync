from __future__ import annotations

import hashlib
import json
import pathlib
import subprocess


HELPER = pathlib.Path(__file__).parents[1] / "helpers" / "fake_gh_attestation.py"


def test_fake_attestation_binds_exact_subject_and_predicate(tmp_path: pathlib.Path) -> None:
    subject = tmp_path / "subject.bin"
    bundle = tmp_path / "bundle.json"
    root = tmp_path / "root.jsonl"
    subject.write_bytes(b"exact subject\n")
    bundle.write_text("{}\n", encoding="utf-8")
    root.write_text("{}\n", encoding="utf-8")
    predicate = "https://slsa.dev/provenance/v1"

    completed = subprocess.run(
        [
            str(HELPER),
            "attestation",
            "verify",
            str(subject),
            "--repo",
            "owner/repository",
            "--signer-workflow",
            "github.com/owner/repository/.github/workflows/release.yml",
            "--source-digest",
            "a" * 40,
            "--source-ref",
            "refs/tags/gds-v1.2.3",
            "--predicate-type",
            predicate,
            "--bundle",
            str(bundle),
            "--custom-trusted-root",
            str(root),
            "--deny-self-hosted-runners",
            "--format",
            "json",
        ],
        check=True,
        capture_output=True,
        text=True,
    )

    result = json.loads(completed.stdout)
    statement = result[0]["verificationResult"]["statement"]
    assert statement["predicateType"] == predicate
    assert statement["subject"][0]["digest"]["sha256"] == hashlib.sha256(
        subject.read_bytes()
    ).hexdigest()


def test_fake_attestation_rejects_incomplete_command(tmp_path: pathlib.Path) -> None:
    subject = tmp_path / "subject.bin"
    subject.write_bytes(b"subject\n")

    completed = subprocess.run(
        [str(HELPER), "attestation", "verify", str(subject)],
        check=False,
        capture_output=True,
        text=True,
    )

    assert completed.returncode == 2
    assert "missing required arguments" in completed.stderr
