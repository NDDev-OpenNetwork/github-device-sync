import base64
import importlib.util
import json
import re
import tempfile
from datetime import datetime, timedelta, timezone
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "harness_evidence_producer", ROOT / "scripts/produce-harness-evidence.py"
)
producer = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(producer)


class HarnessEvidenceProducerTests(unittest.TestCase):
    def test_active_set_matches_the_independent_go_verifier(self):
        body = (ROOT / "core/harnessevidence/evidence.go").read_text()
        block = re.search(r"var ActiveHarnesses = \[\]string\{([^}]+)\}", body).group(1)
        self.assertEqual(tuple(re.findall(r'"([^"]+)"', block)), producer.ACTIVE)

    def test_observation_binds_exact_commit_platform_matrix_and_source_bytes(self):
        commit = "a" * 40
        baseline = json.dumps({"software_artifacts": {"version": "2026.08.25-3e8eec8"}}).encode()
        now = datetime(2026, 9, 12, 12, tzinfo=timezone.utc)
        run = {"run_attempt": 2, "id": 42, "head_sha": commit, "status": "completed", "conclusion": "success", "html_url": "https://example.invalid/run/42"}
        # The harnesses run one lifecycle job per platform and architecture.
        # This fixture named three runner labels, which is what the producer
        # asserted, and both were wrong together — so the test passed while no
        # real run could satisfy the assertion.
        jobs = [{"name": f"lifecycle ({name})", "conclusion": "success", "status": "completed",
                 "run_id": 42, "head_sha": commit, "completed_at": "2026-09-12T11:00:00Z"} for name in (
            "linux/x86_64", "linux/arm64",
            "macos/x86_64", "macos/arm64",
            "windows/x86_64", "windows/arm64",
        )]

        for i, job in enumerate(jobs, 100):
            job["id"] = i

        def fake_gh(path):
            if path == "repos/example/cursor":
                return {"full_name": "example/cursor", "private": False}
            if path.endswith("/commits/main"):
                return {"sha": commit}
            if "/runs?" in path:
                return {"workflow_runs": [run]}
            if path.endswith("/attempts/2/jobs?per_page=100"):
                return {"jobs": jobs}
            raise AssertionError(path)

        def fake_file(_repository, path, _commit):
            return baseline if path.endswith("baseline.json") else path.encode()

        with mock.patch.object(producer, "gh_json", side_effect=fake_gh), mock.patch.object(
            producer, "repository_file", side_effect=fake_file
        ):
            result = producer.observe_harness({
                "id": "cursor", "repository": "example/cursor",
                "baseline": "references/cursor-baseline.json",
                "version_path": ["software_artifacts", "version"],
            }, now=now)
        self.assertEqual(result["run_attempt"], 2)
        self.assertEqual(result["runtime_observed_at"], "2026-09-12T11:00:00Z")
        self.assertEqual(result["module_sha"], commit)
        self.assertEqual(result["executable_version"], "2026.08.25-3e8eec8")
        self.assertRegex(result["suite_cases_digest"], r"^sha256:[0-9a-f]{64}$")

    def test_signatures_are_raw_urlsafe_ed25519(self):
        with tempfile.TemporaryDirectory() as room:
            key = Path(room) / "key.pem"
            producer.subprocess.run(
                ["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(key)],
                check=True, capture_output=True,
            )
            key.chmod(0o600)
            value = producer.sign(key, "test-domain/v1", {"schema_version": 1})
            raw = base64.urlsafe_b64decode(value + "==")
            self.assertEqual(len(raw), 64)
            self.assertEqual(len(producer.public_key(key)), 43)

    def test_typed_digest_preserves_gds_struct_field_order(self):
        payload = {
            "schema_version": 1,
            "manifest_id": "manifest-1",
            "harness_root_sha": "a" * 40,
            "channel": "stable",
            "generated_at": "2026-08-28T00:00:00Z",
            "expires_at": "2026-08-29T00:00:00Z",
            "actor_id": "automation:harness-evidence",
            "evidence": [],
        }
        self.assertEqual(
            producer.digest(payload),
            "sha256:5d7a3c680f6744bd9f3c9e1c592b841c1e617d2c3c3b77a4034d972b747814e0",
        )


class EvidenceBoundaryTests(unittest.TestCase):
    def fixture(self, mutate=None):
        now = datetime(2026, 9, 12, 12, tzinfo=timezone.utc)
        commit = "a" * 40
        jobs = [{"name": f"lifecycle ({platform})", "status": "completed",
                 "conclusion": "success", "head_sha": commit, "run_id": 42,
                 "completed_at": "2026-09-12T11:00:00Z"} for platform in (
                     "linux/x86_64", "linux/arm64", "macos/x86_64", "macos/arm64",
                     "windows/x86_64", "windows/arm64")]
        for i, job in enumerate(jobs, 100):
            job["id"] = i
        if mutate:
            mutate(jobs)
        run = {"id": 42, "run_attempt": 1, "head_sha": commit, "status": "completed",
               "conclusion": "success", "html_url": "https://example.invalid/run/42"}
        def api(path):
            if path == "repos/example/harness":
                return {"full_name": "example/harness", "private": False}
            if path.endswith("/commits/main"):
                return {"sha": commit}
            if "/runs?" in path:
                return {"workflow_runs": [run]}
            if path.endswith("/attempts/1/jobs?per_page=100"):
                return {"jobs": jobs}
            raise AssertionError(path)
        item = {"id": "codex", "repository": "example/harness", "baseline": "baseline.json",
                "version_path": ["version"]}
        return item, now, api

    def test_old_failed_foreign_and_duplicate_jobs_are_not_fresh_proof(self):
        mutations = {
            "stale": lambda j: j[0].update(completed_at="2026-09-09T11:00:00Z"),
            "future": lambda j: j[0].update(completed_at="2026-09-12T13:00:00Z"),
            "missing time": lambda j: j[0].pop("completed_at"),
            "failure": lambda j: j[0].update(conclusion="failure"),
            "foreign source": lambda j: j[0].update(head_sha="b" * 40),
            "foreign run": lambda j: j[0].update(run_id=43),
            "duplicate": lambda j: j.append(dict(j[0])),
            "duplicate identity": lambda j: j[1].update(id=j[0]["id"]),
            "missing identity": lambda j: j[0].pop("id"),
        }
        for name, mutation in mutations.items():
            with self.subTest(name=name):
                item, now, api = self.fixture(mutation)
                with mock.patch.object(producer, "gh_json", side_effect=api):
                    with self.assertRaises(producer.EvidenceError):
                        producer.observe_harness(item, now=now)

    def test_private_repository_cannot_enter_public_evidence(self):
        with mock.patch.object(producer, "gh_json", return_value={"private": True, "full_name": "example/private"}):
            with self.assertRaises(producer.EvidenceError):
                producer.require_public_repository("example/private")

    def test_signer_policy_is_not_extended_or_self_issued(self):
        now = datetime(2026, 9, 12, 12, tzinfo=timezone.utc)
        key = {"algorithm": "ed25519", "key_id": "example-key", "public_key": "public",
               "valid_from": "2026-09-01T00:00:00Z", "valid_until": "2026-10-01T00:00:00Z",
               "status": "active"}
        identity = {"actor_id": "automation:example", "roles": ["harness-evidence", "harness-evidence-aggregate"], "keys": [key]}
        policy = {"identities": [identity]}
        original = json.dumps(policy)
        with mock.patch.object(producer, "public_key", return_value="public"):
            selected, chosen = producer.select_signer(policy, Path("unused"), now)
            self.assertEqual(chosen["valid_until"], "2026-10-01T00:00:00Z")
            self.assertEqual(selected["actor_id"], "automation:example")
            self.assertEqual(json.dumps(policy), original)
            with self.assertRaises(producer.EvidenceError):
                producer.select_signer(policy, Path("unused"), now + timedelta(days=30))
            key["status"] = "revoked"
            with self.assertRaises(producer.EvidenceError):
                producer.select_signer(policy, Path("unused"), now)

    def test_packaging_cannot_extend_runtime_or_signer_lifetime(self):
        now = datetime(2026, 9, 12, 12, tzinfo=timezone.utc)
        observations = [{"runtime_observed_at": "2026-09-10T12:00:00Z"}]
        key = {"valid_until": "2027-01-01T00:00:00Z"}
        self.assertEqual(producer.evidence_expiry(observations, key, now), now + timedelta(hours=24))
        key["valid_until"] = "2026-09-12T13:00:00Z"
        self.assertEqual(producer.evidence_expiry(observations, key, now), now + timedelta(hours=1))
        with self.assertRaises(producer.EvidenceError):
            producer.evidence_expiry(observations, key, now + timedelta(hours=2))


if __name__ == "__main__":
    unittest.main()
