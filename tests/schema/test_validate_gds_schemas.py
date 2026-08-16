from __future__ import annotations

import json
import tempfile
import unittest
from contextlib import redirect_stdout
from io import StringIO
from pathlib import Path

from scripts import validate_gds_schemas as validator


ROOT = Path(__file__).resolve().parents[2]
FIXTURES = ROOT / "tests/fixtures/schemas/v1"
MIGRATION_FIXTURES = ROOT / "tests/fixtures/migrations/v0-to-v1"


class GdsSchemaValidationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.schema_set, cls.schema_findings = validator.load_schema_set(
            ROOT / "schemas/v1"
        )

    def test_schema_set_is_valid_and_complete(self) -> None:
        self.assertEqual([], self.schema_findings)
        self.assertIsNotNone(self.schema_set)
        assert self.schema_set is not None
        self.assertEqual(
            set(validator.SCHEMA_FILES), set(self.schema_set.schemas) - {"common"}
        )

    def test_full_repository_validation_passes(self) -> None:
        result = validator.run_validation(ROOT, FIXTURES / "cases.json")
        self.assertEqual(0, result["exit_code"])
        self.assertEqual("succeeded", result["result"])
        self.assertEqual([], result["findings"])

    def test_release_trust_identity_matches_control_plane_provider(self) -> None:
        self.assertEqual([], validator.validate_release_trust_identity(ROOT))

    def test_release_trust_identity_rejects_stale_owner(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".gds").mkdir()
            (root / "requirements").mkdir()
            (root / ".gds/repository.yaml").write_text(
                "provider:\n  owner: example-org\n  name: github-device-sync\n",
                encoding="utf-8",
            )
            (root / "requirements/bundle-trust.yaml").write_text(
                "source:\n  owner: example-user\n  repository: github-device-sync\n",
                encoding="utf-8",
            )
            findings = validator.validate_release_trust_identity(root)
        self.assertEqual(
            ["GDS_BUNDLE_TRUST_SOURCE_IDENTITY_MISMATCH"],
            [finding.code for finding in findings],
        )

    def test_result_envelope_conforms_to_operation_result_schema(self) -> None:
        assert self.schema_set is not None
        result = validator.result_envelope([], "gds validate schemas")
        errors = list(self.schema_set.validator("operation-result").iter_errors(result))
        self.assertEqual([], errors)

    def test_input_contract_error_uses_input_exit_class(self) -> None:
        result = validator.result_envelope(
            [validator.Finding("GDS_YAML_PARSE_FAILED", "high", "invalid yaml")],
            "gds validate schemas",
        )
        self.assertEqual("input", result["exit_class"])
        self.assertEqual(validator.EXIT_INPUT, result["exit_code"])

    def test_valid_yaml_boolean_is_not_treated_as_ambiguous(self) -> None:
        data = validator.load_data(FIXTURES / "valid-policy.yaml")
        self.assertIs(
            data["apply"]["security"]["external_write_requires_approval"], True
        )

    def test_ambiguous_plain_scalar_is_rejected(self) -> None:
        with self.assertRaisesRegex(
            validator.InputContractError, "Ambiguous plain scalar"
        ) as context:
            validator.load_data(FIXTURES / "invalid-yaml-ambiguous-scalar.yaml")
        self.assertEqual("GDS_YAML_AMBIGUOUS_SCALAR", context.exception.code)

    def test_anchor_is_rejected(self) -> None:
        with self.assertRaises(validator.InputContractError) as context:
            validator.load_data(FIXTURES / "invalid-yaml-anchor.yaml")
        self.assertEqual("GDS_YAML_ANCHOR_FORBIDDEN", context.exception.code)

    def test_explicit_yaml_tag_is_rejected(self) -> None:
        path = self._write_temp_yaml("value: !!str tagged\n")
        with self.assertRaises(validator.InputContractError) as context:
            validator.load_data(path)
        self.assertEqual("GDS_YAML_EXPLICIT_TAG_FORBIDDEN", context.exception.code)

    def test_alias_is_rejected(self) -> None:
        path = self._write_temp_yaml("root:\n  copy: *missing\n")
        with self.assertRaises(validator.InputContractError) as context:
            validator.load_data(path)
        self.assertEqual("GDS_YAML_ALIAS_FORBIDDEN", context.exception.code)

    def test_merge_key_is_rejected(self) -> None:
        path = self._write_temp_yaml("root:\n  <<: {value: 1}\n")
        with self.assertRaises(validator.InputContractError) as context:
            validator.load_data(path)
        self.assertEqual("GDS_YAML_MERGE_KEY_FORBIDDEN", context.exception.code)

    def test_duplicate_yaml_key_is_rejected(self) -> None:
        with self.assertRaises(validator.InputContractError) as context:
            validator.load_data(FIXTURES / "invalid-yaml-duplicate-key.yaml")
        self.assertEqual("GDS_YAML_DUPLICATE_KEY", context.exception.code)

    def test_duplicate_json_key_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "duplicate.json"
            path.write_text('{"key": 1, "key": 2}\n', encoding="utf-8")
            with self.assertRaises(validator.InputContractError) as context:
                validator.load_data(path)
        self.assertEqual("GDS_JSON_DUPLICATE_KEY", context.exception.code)

    def test_oversized_input_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "oversized.json"
            path.write_bytes(b" " * (validator.MAX_INPUT_BYTES + 1))
            with self.assertRaises(validator.InputContractError) as context:
                validator.load_data(path)
        self.assertEqual("GDS_INPUT_TOO_LARGE", context.exception.code)

    def test_duplicate_migration_id_is_a_semantic_error(self) -> None:
        assert self.schema_set is not None
        findings = validator.validate_instance(
            self.schema_set,
            "migration-registry",
            FIXTURES / "invalid-migration-duplicate-id.yaml",
        )
        self.assertIn("GDS_MIGRATION_DUPLICATE_ID", {item.code for item in findings})

    def test_duplicate_source_id_is_a_semantic_error(self) -> None:
        assert self.schema_set is not None
        findings = validator.validate_instance(
            self.schema_set,
            "source-register",
            FIXTURES / "invalid-source-register-duplicate-id.yaml",
        )
        self.assertIn(
            "GDS_SOURCE_REGISTER_DUPLICATE_ID", {item.code for item in findings}
        )

    def test_plan_semantics_reject_expiry_before_creation(self) -> None:
        assert self.schema_set is not None
        plan = validator.load_data(FIXTURES / "valid-plan.json")
        plan["expires_at"] = plan["created_at"]
        path = self._write_temp_json(plan)
        findings = validator.validate_instance(self.schema_set, "plan", path)
        self.assertIn("GDS_PLAN_EXPIRY_INVALID", {item.code for item in findings})

    def test_date_time_format_is_enforced_without_optional_extras(self) -> None:
        assert self.schema_set is not None
        plan = validator.load_data(FIXTURES / "valid-plan.json")
        plan["created_at"] = "not-a-date-time"
        path = self._write_temp_json(plan)
        findings = validator.validate_instance(self.schema_set, "plan", path)
        self.assertIn("GDS_INSTANCE_INVALID", {item.code for item in findings})

    def test_plan_semantics_reject_step_outside_scope(self) -> None:
        assert self.schema_set is not None
        plan = validator.load_data(FIXTURES / "valid-plan.json")
        plan["steps"][0]["repository_id"] = "repo_01JEXAMPZ0000000000000000F"
        path = self._write_temp_json(plan)
        findings = validator.validate_instance(self.schema_set, "plan", path)
        self.assertIn("GDS_PLAN_STEP_OUTSIDE_SCOPE", {item.code for item in findings})

    def test_device_semantics_reject_unknown_workspace_root(self) -> None:
        assert self.schema_set is not None
        device = validator.load_data(FIXTURES / "valid-device.yaml")
        device["materialization"]["include"][0]["workspace_root"] = "missing"
        path = self._write_temp_json(device)
        findings = validator.validate_instance(self.schema_set, "device", path)
        self.assertIn(
            "GDS_DEVICE_WORKSPACE_ROOT_UNKNOWN", {item.code for item in findings}
        )

    def test_device_semantics_reject_duplicate_selector(self) -> None:
        assert self.schema_set is not None
        device = validator.load_data(FIXTURES / "valid-device.yaml")
        device["materialization"]["include"].append(
            dict(device["materialization"]["include"][0])
        )
        path = self._write_temp_json(device)
        findings = validator.validate_instance(self.schema_set, "device", path)
        self.assertIn("GDS_DEVICE_SELECTOR_DUPLICATE", {item.code for item in findings})

    def test_migration_fixture_preserves_provider_and_git_topology(self) -> None:
        assert self.schema_set is not None
        legacy = validator.load_data(MIGRATION_FIXTURES / "legacy-superproject.json")
        expected = validator.load_data(MIGRATION_FIXTURES / "expected-repository.yaml")

        self.assertEqual(
            [], list(self.schema_set.validator("repository").iter_errors(expected))
        )
        self.assertEqual(
            legacy["github"],
            {
                "repository_id": expected["provider"]["repository_id"],
                "owner": expected["provider"]["owner"],
                "name": expected["provider"]["name"],
            },
        )
        self.assertEqual(
            legacy["git"]["submodules"][0]["name"],
            expected["relationships"][0]["gitmodules_name"],
        )
        self.assertEqual(
            legacy["git"]["submodules"][0]["target_gds_id"],
            expected["relationships"][0]["target"],
        )
        self.assertEqual(expected, json.loads(json.dumps(expected, sort_keys=True)))

    def test_fixture_index_contains_all_contract_types(self) -> None:
        document = json.loads((FIXTURES / "cases.json").read_text(encoding="utf-8"))
        valid_schemas = {case["schema"] for case in document["cases"] if case["valid"]}
        self.assertEqual(set(validator.SCHEMA_FILES), valid_schemas)

    def test_invalid_root_returns_input_exit_class(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            missing = Path(directory) / "missing"
            with redirect_stdout(StringIO()):
                exit_code = validator.main(["--root", str(missing), "--json"])
        self.assertEqual(validator.EXIT_INPUT, exit_code)

    def _write_temp_yaml(self, text: str) -> Path:
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        path = Path(directory.name) / "input.yaml"
        path.write_text(text, encoding="utf-8")
        return path

    def _write_temp_json(self, value: object) -> Path:
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        path = Path(directory.name) / "input.json"
        path.write_text(json.dumps(value), encoding="utf-8")
        return path


if __name__ == "__main__":
    unittest.main()
