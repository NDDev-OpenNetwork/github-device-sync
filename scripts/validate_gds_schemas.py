#!/usr/bin/env python3
"""Validate GDS v1 schemas, canonical manifests, and schema fixtures.

This command is deliberately read-only. It rejects YAML features whose meaning
is not portable across the parsers targeted by GDS before JSON Schema
validation begins.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from dataclasses import dataclass
from datetime import date, datetime
from pathlib import Path
from typing import Any, Iterable, Mapping, Sequence
from urllib.parse import urlsplit

import yaml
from jsonschema import Draft202012Validator, FormatChecker
from jsonschema.exceptions import SchemaError
from referencing import Registry, Resource
from referencing.exceptions import Unresolvable
from yaml.nodes import MappingNode
from yaml.tokens import (
    AliasToken,
    AnchorToken,
    BlockEndToken,
    BlockMappingStartToken,
    BlockSequenceStartToken,
    FlowMappingEndToken,
    FlowMappingStartToken,
    FlowSequenceEndToken,
    FlowSequenceStartToken,
    ScalarToken,
    TagToken,
)


SCHEMA_VERSION = 1
EXIT_SUCCESS = 0
EXIT_VALIDATION = 2
EXIT_INPUT = 4
EXIT_INTERNAL = 14
MAX_INPUT_BYTES = 4 << 20
MAX_NESTING = 128
MAX_YAML_TOKENS = 200_000

INPUT_FINDING_PREFIXES = (
    "GDS_INPUT_",
    "GDS_JSON_",
    "GDS_SCHEMA_",
    "GDS_YAML_",
)
INPUT_FINDING_CODES = {
    "GDS_FIXTURE_CASE_INVALID",
    "GDS_FIXTURE_INDEX_INVALID",
    "GDS_FIXTURE_PATH_OUTSIDE_ROOT",
}

SCHEMA_FILES = {
    "approval": "approval.schema.json",
    "assurance-report": "assurance-report.schema.json",
    "audit-snapshot": "audit-snapshot.schema.json",
    "bundle-lock": "bundle-lock.schema.json",
    "bundle-manifest": "bundle-manifest.schema.json",
    "bundle-trust": "bundle-trust.schema.json",
    "compiled-policy": "compiled-policy.schema.json",
    "controller-runtime": "controller-runtime.schema.json",
    "repository": "repository.schema.json",
    "release-envelope": "release-envelope.schema.json",
    "release-failure-envelope": "release-failure-envelope.schema.json",
    "release-installation": "release-installation.schema.json",
    "rollout": "rollout.schema.json",
    "rollout-request": "rollout-request.schema.json",
    "rollback-authorization": "rollback-authorization.schema.json",
    "estate": "estate.schema.json",
    "estate-registration": "estate-registration.schema.json",
    "runtime-dependencies": "runtime-dependencies.schema.json",
    "installation": "installation.schema.json",
    "mutation-capability": "mutation-capability.schema.json",
    "github-runtime": "github-runtime.schema.json",
    "github-mutation-runtime": "github-mutation-runtime.schema.json",
    "owner": "owner.schema.json",
    "selector": "selector.schema.json",
    "policy": "policy.schema.json",
    "policy-exception": "policy-exception.schema.json",
    "skill-registry": "skill-registry.schema.json",
    "source-register": "source-register.schema.json",
    "harness-registry": "harness-registry.schema.json",
    "harness-eval-run": "harness-eval-run.schema.json",
    "harness-runtime-evidence": "harness-runtime-evidence.schema.json",
    "harness-runtime-contract": "harness-runtime-contract.schema.json",
    "harness-profile": "harness-profile.schema.json",
    "module-harness-bridge": "module-harness-bridge.schema.json",
    "skill-trigger-eval": "skill-trigger-eval.schema.json",
    "skill-output-eval": "skill-output-eval.schema.json",
    "skill-enforcement-eval": "skill-enforcement-eval.schema.json",
    "memory-metadata": "memory-metadata.schema.json",
    "device": "device.schema.json",
    "field-ownership": "field-ownership.schema.json",
    "freshness-policy": "freshness-policy.schema.json",
    "device-evidence": "device-evidence.schema.json",
    "delegated-harness-evidence": "delegated-harness-evidence.schema.json",
    "harness-runtime-manifest": "harness-runtime-manifest.schema.json",
    "plan": "plan.schema.json",
    "plan-enablement": "plan-enablement.schema.json",
    "operation-result": "operation-result.schema.json",
    "portfolio-plan": "portfolio-plan.schema.json",
    "migration-registry": "migration-registry.schema.json",
    "trust-policy": "trust-policy.schema.json",
}

AMBIGUOUS_PLAIN_SCALARS = {
    "y",
    "yes",
    "n",
    "no",
    "on",
    "off",
}

RFC3339_PATTERN = re.compile(
    r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$"
)


class InputContractError(ValueError):
    """An input file violates the portable serialization contract."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code


class UniqueKeySafeLoader(yaml.SafeLoader):
    """SafeLoader variant that rejects duplicate and merge mapping keys."""

    def construct_mapping(
        self, node: MappingNode, deep: bool = False
    ) -> dict[Any, Any]:
        if not isinstance(node, MappingNode):
            raise InputContractError(
                "GDS_YAML_MAPPING_INVALID",
                f"Expected a mapping node, received {type(node).__name__}",
            )

        mapping: dict[Any, Any] = {}
        for key_node, value_node in node.value:
            if key_node.tag == "tag:yaml.org,2002:merge" or key_node.value == "<<":
                raise InputContractError(
                    "GDS_YAML_MERGE_KEY_FORBIDDEN",
                    f"YAML merge key is forbidden at line {key_node.start_mark.line + 1}",
                )

            key = self.construct_object(key_node, deep=deep)
            try:
                duplicate = key in mapping
            except TypeError as exc:
                raise InputContractError(
                    "GDS_YAML_MAPPING_KEY_INVALID",
                    f"Unhashable YAML mapping key at line {key_node.start_mark.line + 1}",
                ) from exc

            if duplicate:
                raise InputContractError(
                    "GDS_YAML_DUPLICATE_KEY",
                    f"Duplicate YAML key {key!r} at line {key_node.start_mark.line + 1}",
                )
            mapping[key] = self.construct_object(value_node, deep=deep)
        return mapping


UniqueKeySafeLoader.add_constructor(
    yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG,
    UniqueKeySafeLoader.construct_mapping,
)


def build_format_checker() -> FormatChecker:
    """Build deterministic checks without optional jsonschema format extras."""

    checker = FormatChecker()

    @checker.checks("date", raises=ValueError)
    def is_date(value: object) -> bool:
        return isinstance(value, str) and date.fromisoformat(value) is not None

    @checker.checks("date-time", raises=ValueError)
    def is_datetime(value: object) -> bool:
        if not isinstance(value, str) or RFC3339_PATTERN.fullmatch(value) is None:
            return False
        return datetime.fromisoformat(value.replace("Z", "+00:00")) is not None

    @checker.checks("uri", raises=ValueError)
    def is_uri(value: object) -> bool:
        if not isinstance(value, str) or any(
            character.isspace() for character in value
        ):
            return False
        parsed = urlsplit(value)
        if not parsed.scheme:
            return False
        return parsed.scheme not in {"http", "https"} or bool(parsed.netloc)

    return checker


def _json_pairs_no_duplicates(pairs: Sequence[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise InputContractError(
                "GDS_JSON_DUPLICATE_KEY", f"Duplicate JSON key {key!r}"
            )
        result[key] = value
    return result


def _scan_yaml_contract(text: str, path: Path) -> None:
    try:
        tokens = yaml.scan(text, Loader=UniqueKeySafeLoader)
        token_count = 0
        depth = 0
        for token in tokens:
            token_count += 1
            if token_count > MAX_YAML_TOKENS:
                raise InputContractError(
                    "GDS_YAML_NODE_LIMIT_EXCEEDED",
                    f"YAML token count exceeds {MAX_YAML_TOKENS} in {path}",
                )
            if isinstance(
                token,
                (
                    BlockMappingStartToken,
                    BlockSequenceStartToken,
                    FlowMappingStartToken,
                    FlowSequenceStartToken,
                ),
            ):
                depth += 1
                if depth > MAX_NESTING:
                    raise InputContractError(
                        "GDS_INPUT_NESTING_EXCEEDED",
                        f"YAML nesting exceeds {MAX_NESTING} in {path}",
                    )
            elif isinstance(
                token,
                (BlockEndToken, FlowMappingEndToken, FlowSequenceEndToken),
            ):
                depth = max(0, depth - 1)
            if isinstance(token, AnchorToken):
                raise InputContractError(
                    "GDS_YAML_ANCHOR_FORBIDDEN",
                    f"YAML anchor is forbidden in {path} at line "
                    f"{token.start_mark.line + 1}",
                )
            if isinstance(token, AliasToken):
                raise InputContractError(
                    "GDS_YAML_ALIAS_FORBIDDEN",
                    f"YAML alias is forbidden in {path} at line "
                    f"{token.start_mark.line + 1}",
                )
            if isinstance(token, TagToken):
                raise InputContractError(
                    "GDS_YAML_EXPLICIT_TAG_FORBIDDEN",
                    f"Explicit YAML tag is forbidden in {path} at line "
                    f"{token.start_mark.line + 1}",
                )
            if (
                isinstance(token, ScalarToken)
                and token.style is None
                and token.value.lower() in AMBIGUOUS_PLAIN_SCALARS
            ):
                raise InputContractError(
                    "GDS_YAML_AMBIGUOUS_SCALAR",
                    f"Ambiguous plain scalar {token.value!r} in {path} at line "
                    f"{token.start_mark.line + 1}; quote it or use JSON spelling",
                )
    except yaml.YAMLError as exc:
        raise InputContractError(
            "GDS_YAML_PARSE_FAILED", f"Cannot scan YAML {path}: {exc}"
        ) from exc


def load_data(path: Path) -> Any:
    """Load JSON/YAML with duplicate-key and portability enforcement."""

    try:
        raw = path.read_bytes()
    except OSError as exc:
        raise InputContractError(
            "GDS_INPUT_READ_FAILED", f"Cannot read {path}: {exc}"
        ) from exc
    if len(raw) > MAX_INPUT_BYTES:
        raise InputContractError(
            "GDS_INPUT_TOO_LARGE",
            f"Input {path} exceeds the {MAX_INPUT_BYTES}-byte limit",
        )
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise InputContractError(
            "GDS_INPUT_ENCODING_INVALID", f"Input {path} is not UTF-8: {exc}"
        ) from exc

    try:
        if path.suffix == ".json":
            return json.loads(text, object_pairs_hook=_json_pairs_no_duplicates)
        if path.suffix in {".yaml", ".yml"}:
            _scan_yaml_contract(text, path)
            return yaml.load(text, Loader=UniqueKeySafeLoader)
    except InputContractError:
        raise
    except RecursionError as exc:
        raise InputContractError(
            "GDS_INPUT_NESTING_EXCEEDED",
            f"Input nesting exceeds the parser limit in {path}",
        ) from exc
    except (json.JSONDecodeError, yaml.YAMLError) as exc:
        raise InputContractError(
            "GDS_INPUT_PARSE_FAILED", f"Cannot parse {path}: {exc}"
        ) from exc

    raise InputContractError(
        "GDS_INPUT_FORMAT_UNSUPPORTED",
        f"Unsupported input format for {path}; expected .json, .yaml, or .yml",
    )


@dataclass(frozen=True)
class Finding:
    code: str
    severity: str
    message: str
    evidence: Mapping[str, Any] | None = None

    def to_dict(self) -> dict[str, Any]:
        value: dict[str, Any] = {
            "code": self.code,
            "severity": self.severity,
            "message": self.message,
        }
        if self.evidence:
            value["evidence"] = dict(self.evidence)
        return value


@dataclass(frozen=True)
class SchemaSet:
    schemas: Mapping[str, Mapping[str, Any]]
    registry: Registry

    def validator(self, schema_name: str) -> Draft202012Validator:
        try:
            schema = self.schemas[schema_name]
        except KeyError as exc:
            raise InputContractError(
                "GDS_SCHEMA_NAME_UNKNOWN", f"Unknown schema name {schema_name!r}"
            ) from exc
        return Draft202012Validator(
            schema,
            registry=self.registry,
            format_checker=build_format_checker(),
        )


def load_schema_set(schema_dir: Path) -> tuple[SchemaSet | None, list[Finding]]:
    findings: list[Finding] = []
    schemas: dict[str, Mapping[str, Any]] = {}
    registry = Registry()

    paths = sorted(schema_dir.glob("*.schema.json"))
    expected = {"common.schema.json", *SCHEMA_FILES.values()}
    actual = {path.name for path in paths}
    missing = sorted(expected - actual)
    if missing:
        findings.append(
            Finding(
                "GDS_SCHEMA_FILE_MISSING",
                "high",
                "Required schema files are missing",
                {"schema_dir": str(schema_dir), "missing": missing},
            )
        )

    for path in paths:
        try:
            schema = load_data(path)
            if not isinstance(schema, Mapping):
                raise InputContractError(
                    "GDS_SCHEMA_ROOT_INVALID",
                    f"Schema root in {path} must be an object",
                )
            Draft202012Validator.check_schema(schema)
            schema_id = schema.get("$id")
            if not isinstance(schema_id, str) or not schema_id:
                raise InputContractError(
                    "GDS_SCHEMA_ID_MISSING", f"Schema {path} has no non-empty $id"
                )
            registry = registry.with_resource(
                schema_id, Resource.from_contents(dict(schema))
            )
            logical_name = next(
                (
                    name
                    for name, filename in SCHEMA_FILES.items()
                    if filename == path.name
                ),
                "common" if path.name == "common.schema.json" else path.stem,
            )
            schemas[logical_name] = schema
        except InputContractError as exc:
            findings.append(Finding(exc.code, "high", str(exc), {"path": str(path)}))
        except SchemaError as exc:
            findings.append(
                Finding(
                    "GDS_SCHEMA_INVALID",
                    "high",
                    f"Invalid Draft 2020-12 schema {path}: {exc.message}",
                    {"path": str(path), "schema_path": list(exc.path)},
                )
            )
        except Exception as exc:  # defensive: schema bootstrap must report the file
            findings.append(
                Finding(
                    "GDS_SCHEMA_LOAD_FAILED",
                    "high",
                    f"Cannot load schema {path}: {exc}",
                    {"path": str(path)},
                )
            )

    if findings:
        return None, findings
    return SchemaSet(schemas=schemas, registry=registry), []


def _json_pointer(parts: Iterable[Any]) -> str:
    encoded = [str(part).replace("~", "~0").replace("/", "~1") for part in parts]
    return "/" + "/".join(encoded) if encoded else "/"


def _semantic_findings(schema_name: str, instance: Any, path: Path) -> list[Finding]:
    findings: list[Finding] = []
    if schema_name == "device" and isinstance(instance, Mapping):
        workspace_roots = instance.get("workspace_roots", {})
        known_roots = (
            set(workspace_roots) if isinstance(workspace_roots, Mapping) else set()
        )
        materialization = instance.get("materialization", {})
        include = (
            materialization.get("include", [])
            if isinstance(materialization, Mapping)
            else []
        )
        selectors: set[str] = set()
        used_roots: dict[str, str] = {}
        for index, assignment in enumerate(include):
            if not isinstance(assignment, Mapping):
                continue
            selector = assignment.get("selector")
            workspace_root = assignment.get("workspace_root")
            if isinstance(selector, str):
                if selector in selectors:
                    findings.append(
                        Finding(
                            "GDS_DEVICE_SELECTOR_DUPLICATE",
                            "high",
                            f"Device selector {selector!r} occurs more than once in {path}",
                            {"path": str(path), "index": index, "selector": selector},
                        )
                    )
                selectors.add(selector)
            if isinstance(workspace_root, str) and workspace_root not in known_roots:
                findings.append(
                    Finding(
                        "GDS_DEVICE_WORKSPACE_ROOT_UNKNOWN",
                        "high",
                        f"Device assignment in {path} references an unknown workspace root",
                        {
                            "path": str(path),
                            "index": index,
                            "workspace_root": workspace_root,
                        },
                    )
                )
            if isinstance(workspace_root, str) and isinstance(selector, str):
                previous_selector = used_roots.get(workspace_root)
                if previous_selector is not None and previous_selector != selector:
                    findings.append(
                        Finding(
                            "GDS_DEVICE_WORKSPACE_ROOT_REUSED",
                            "high",
                            f"Device workspace root {workspace_root!r} is assigned to multiple selectors in {path}",
                            {
                                "path": str(path),
                                "index": index,
                                "workspace_root": workspace_root,
                                "selector": selector,
                                "previous_selector": previous_selector,
                            },
                        )
                    )
                else:
                    used_roots[workspace_root] = selector

        # Device-class cross-field rules. These mirror the platform/profile/gui/
        # docker rules enforced by modules/macos-ubuntu-bootstrap/scripts/bootstrap.sh
        # and core/validation/schema.go deviceClassFindings, so a device descriptor
        # and the OS installer it drives cannot disagree. They only fire when a
        # class block is present; a descriptor that omits it stays valid.
        device = instance.get("device", {})
        if isinstance(device, Mapping) and isinstance(device.get("class"), Mapping):
            device_class = device["class"]
            os_name = device.get("os", "")
            profile = device_class.get("profile", "")
            gui = device_class.get("gui", "")
            docker_mode = device_class.get("docker_mode", "")
            execution_policy = device_class.get("execution_policy", "")
            hardening = device_class.get("hardening")
            # macOS only supports the desktop profile and never local Docker.
            if os_name == "macos":
                if profile and profile != "desktop":
                    findings.append(
                        Finding(
                            "GDS_DEVICE_CLASS_MACOS_CONFLICT",
                            "high",
                            "macOS only supports the desktop device class profile.",
                            {"path": str(path), "os": os_name, "profile": profile},
                        )
                    )
                if docker_mode and docker_mode != "none":
                    findings.append(
                        Finding(
                            "GDS_DEVICE_CLASS_MACOS_CONFLICT",
                            "high",
                            "macOS never installs local Docker; docker_mode must be none.",
                            {"path": str(path), "os": os_name, "docker_mode": docker_mode},
                        )
                    )
            # The server profile is always headless.
            if profile == "server" and gui and gui != "disabled":
                findings.append(
                    Finding(
                        "GDS_DEVICE_CLASS_SERVER_GUI",
                        "high",
                        "The server device class profile is always headless; gui must be disabled.",
                        {"path": str(path), "profile": profile, "gui": gui},
                    )
                )
            # The desktop profile does not install Docker.
            # desktop-builds is the profile for local development with Docker.
            if profile == "desktop" and docker_mode and docker_mode != "none":
                findings.append(
                    Finding(
                        "GDS_DEVICE_CLASS_DESKTOP_DOCKER",
                        "high",
                        "The desktop device class profile does not install Docker; docker_mode must be none. Use profile desktop-builds for local Docker.",
                        {"path": str(path), "profile": profile, "docker_mode": docker_mode},
                    )
                )
            if profile == "desktop-builds" and docker_mode and docker_mode != "rootful":
                findings.append(
                    Finding(
                        "GDS_DEVICE_CLASS_DESKTOP_BUILDS_DOCKER",
                        "high",
                        "The desktop-builds profile requires docker_mode rootful.",
                        {"path": str(path), "profile": profile, "docker_mode": docker_mode},
                    )
                )
            # execution_policy, when declared, must match the profile.
            if execution_policy and profile:
                _POLICY_MAP = {
                    "desktop": "source-lsp-only",
                    "desktop-builds": "local-dev-with-builds",
                    "server": "container-execution-only",
                }
                expected = _POLICY_MAP.get(profile)
                if expected and execution_policy != expected:
                    findings.append(
                        Finding(
                            "GDS_DEVICE_CLASS_EXECUTION_POLICY",
                            "high",
                            "Device class execution_policy must match the profile.",
                            {
                                "path": str(path),
                                "profile": profile,
                                "execution_policy": execution_policy,
                                "expected": expected,
                            },
                        )
                    )
            # Hardening toggles are server-only.
            if (
                isinstance(hardening, Mapping)
                and hardening
                and profile
                and profile != "server"
            ):
                findings.append(
                    Finding(
                        "GDS_DEVICE_CLASS_HARDENING_PROFILE",
                        "high",
                        "Device class hardening is only permitted with the server profile.",
                        {"path": str(path), "profile": profile, "hardening": hardening},
                    )
                )

    if schema_name == "source-register" and isinstance(instance, Mapping):
        source_ids: set[str] = set()
        for index, source in enumerate(instance.get("sources", [])):
            if not isinstance(source, Mapping):
                continue
            source_id = source.get("id")
            if isinstance(source_id, str):
                if source_id in source_ids:
                    findings.append(
                        Finding(
                            "GDS_SOURCE_REGISTER_DUPLICATE_ID",
                            "high",
                            f"Source id {source_id!r} occurs more than once in {path}",
                            {"path": str(path), "index": index, "id": source_id},
                        )
                    )
                source_ids.add(source_id)
            verified_at = source.get("verified_at")
            next_review = source.get("next_review")
            if (
                isinstance(verified_at, str)
                and isinstance(next_review, str)
                and next_review < verified_at
            ):
                findings.append(
                    Finding(
                        "GDS_SOURCE_REGISTER_REVIEW_ORDER_INVALID",
                        "high",
                        f"Source {source_id!r} review date precedes verification date",
                        {"path": str(path), "index": index, "id": source_id},
                    )
                )

    if schema_name == "compiled-policy" and isinstance(instance, Mapping):
        effective = instance.get("effective", {})
        provenance = instance.get("provenance", {})
        expected_pointers: set[str] = set()
        _collect_leaf_pointers(effective, ["effective"], expected_pointers)
        actual_pointers = set(provenance) if isinstance(provenance, Mapping) else set()
        for pointer in sorted(expected_pointers - actual_pointers):
            findings.append(
                Finding(
                    "GDS_COMPILED_POLICY_PROVENANCE_MISSING",
                    "high",
                    f"Compiled policy leaf {pointer} in {path} has no provenance",
                    {"path": str(path), "pointer": pointer},
                )
            )
        for pointer in sorted(actual_pointers - expected_pointers):
            findings.append(
                Finding(
                    "GDS_COMPILED_POLICY_PROVENANCE_ORPHAN",
                    "high",
                    f"Provenance {pointer} in {path} does not identify an effective leaf",
                    {"path": str(path), "pointer": pointer},
                )
            )

        source_refs: dict[str, Mapping[str, Any]] = {}
        source_order: list[tuple[int, int, str]] = []
        tier_order = {
            "base": 0,
            "owner": 1,
            "portfolio": 2,
            "role": 3,
            "stack": 4,
            "lifecycle": 5,
            "repository": 6,
        }
        for index, source_ref in enumerate(instance.get("sources", [])):
            if not isinstance(source_ref, Mapping) or not isinstance(
                source_ref.get("id"), str
            ):
                continue
            source_id = source_ref["id"]
            if source_id in source_refs:
                findings.append(
                    Finding(
                        "GDS_COMPILED_POLICY_DUPLICATE_SOURCE",
                        "high",
                        f"Compiled policy source {source_id!r} occurs more than once in {path}",
                        {"path": str(path), "id": source_id, "index": index},
                    )
                )
            source_refs[source_id] = source_ref
            source_order.append(
                (
                    tier_order.get(str(source_ref.get("tier")), 99),
                    int(source_ref.get("priority", -1)),
                    source_id,
                )
            )
        if source_order != sorted(source_order):
            findings.append(
                Finding(
                    "GDS_COMPILED_POLICY_SOURCE_ORDER_INVALID",
                    "high",
                    f"Compiled policy sources in {path} are not in tier, priority, and id order",
                    {"path": str(path)},
                )
            )
        if isinstance(provenance, Mapping):
            for pointer, raw_entry in sorted(provenance.items()):
                if not isinstance(raw_entry, Mapping):
                    continue
                source_ref = source_refs.get(raw_entry.get("source"))
                if (
                    not isinstance(source_ref, Mapping)
                    or source_ref.get("tier") != raw_entry.get("tier")
                    or source_ref.get("priority") != raw_entry.get("priority")
                    or source_ref.get("path") != raw_entry.get("file")
                ):
                    findings.append(
                        Finding(
                            "GDS_COMPILED_POLICY_PROVENANCE_SOURCE_INVALID",
                            "high",
                            f"Provenance {pointer} in {path} does not match a compiled source",
                            {"path": str(path), "pointer": pointer},
                        )
                    )

        metadata = instance.get("compiled_policy", {})
        if isinstance(metadata, Mapping):
            payload = {
                "schema_version": instance.get("schema_version"),
                "repository_id": metadata.get("repository_id"),
                "bundle_version": metadata.get("bundle_version"),
                "sources": instance.get("sources"),
                "effective": effective,
                "provenance": provenance,
            }
            expected_digest = _sha256_json(payload)
            if metadata.get("digest") != expected_digest:
                findings.append(
                    Finding(
                        "GDS_COMPILED_POLICY_DIGEST_MISMATCH",
                        "high",
                        f"Compiled policy digest in {path} does not match its canonical payload",
                        {
                            "path": str(path),
                            "expected": expected_digest,
                            "observed": metadata.get("digest"),
                        },
                    )
                )

    if schema_name == "bundle-lock" and isinstance(instance, Mapping):
        projection = instance.get("projection", {})
        files = projection.get("files", []) if isinstance(projection, Mapping) else []
        paths = [
            item.get("path")
            for item in files
            if isinstance(item, Mapping) and isinstance(item.get("path"), str)
        ]
        if len(paths) != len(set(paths)):
            findings.append(
                Finding(
                    "GDS_BUNDLE_LOCK_DUPLICATE_PATH",
                    "high",
                    f"Bundle lock {path} contains duplicate projection paths",
                    {"path": str(path)},
                )
            )
        if paths != sorted(paths):
            findings.append(
                Finding(
                    "GDS_BUNDLE_LOCK_FILE_ORDER_INVALID",
                    "high",
                    f"Bundle lock {path} paths are not lexicographically ordered",
                    {"path": str(path), "paths": paths},
                )
            )
        if isinstance(projection, Mapping):
            expected_digest = _sha256_json(files)
            if projection.get("output_digest") != expected_digest:
                findings.append(
                    Finding(
                        "GDS_BUNDLE_LOCK_OUTPUT_DIGEST_MISMATCH",
                        "high",
                        f"Bundle lock output digest in {path} does not match its file list",
                        {
                            "path": str(path),
                            "expected": expected_digest,
                            "observed": projection.get("output_digest"),
                        },
                    )
                )

    if schema_name == "migration-registry" and isinstance(instance, Mapping):
        seen: set[str] = set()
        for index, migration in enumerate(instance.get("migrations", [])):
            if not isinstance(migration, Mapping) or not isinstance(
                migration.get("id"), str
            ):
                continue
            migration_id = migration["id"]
            if migration_id in seen:
                findings.append(
                    Finding(
                        "GDS_MIGRATION_DUPLICATE_ID",
                        "high",
                        f"Duplicate migration id {migration_id!r} in {path}",
                        {"path": str(path), "index": index, "id": migration_id},
                    )
                )
            seen.add(migration_id)
            source_version = migration.get("from")
            target_version = migration.get("to")
            if (
                isinstance(source_version, int)
                and isinstance(target_version, int)
                and source_version >= target_version
            ):
                findings.append(
                    Finding(
                        "GDS_MIGRATION_DIRECTION_INVALID",
                        "high",
                        f"Migration {migration_id!r} must increase schema version",
                        {
                            "path": str(path),
                            "index": index,
                            "from": source_version,
                            "to": target_version,
                        },
                    )
                )

    if schema_name == "plan" and isinstance(instance, Mapping):
        created_at = instance.get("created_at")
        expires_at = instance.get("expires_at")
        if isinstance(created_at, str) and isinstance(expires_at, str):
            try:
                created = datetime.fromisoformat(created_at.replace("Z", "+00:00"))
                expires = datetime.fromisoformat(expires_at.replace("Z", "+00:00"))
                if expires <= created:
                    findings.append(
                        Finding(
                            "GDS_PLAN_EXPIRY_INVALID",
                            "high",
                            f"Plan expiry in {path} must be later than creation time",
                            {
                                "path": str(path),
                                "created_at": created_at,
                                "expires_at": expires_at,
                            },
                        )
                    )
            except ValueError:
                pass  # JSON Schema format validation owns malformed timestamps.

        scope = instance.get("scope")
        scope_ids = (
            set(scope.get("repositories", [])) if isinstance(scope, Mapping) else set()
        )
        preconditions = instance.get("preconditions", [])
        precondition_ids = [
            item.get("repository_id")
            for item in preconditions
            if isinstance(item, Mapping) and isinstance(item.get("repository_id"), str)
        ]
        if scope_ids and (
            set(precondition_ids) != scope_ids
            or len(precondition_ids) != len(scope_ids)
        ):
            findings.append(
                Finding(
                    "GDS_PLAN_PRECONDITION_SCOPE_MISMATCH",
                    "high",
                    f"Plan preconditions in {path} must cover every scoped repository once",
                    {
                        "path": str(path),
                        "scope_repositories": sorted(scope_ids),
                        "precondition_repositories": precondition_ids,
                    },
                )
            )

        step_ids: set[str] = set()
        for index, step in enumerate(instance.get("steps", [])):
            if not isinstance(step, Mapping):
                continue
            step_id = step.get("step_id")
            if isinstance(step_id, str):
                if step_id in step_ids:
                    findings.append(
                        Finding(
                            "GDS_PLAN_DUPLICATE_STEP_ID",
                            "high",
                            f"Duplicate plan step id {step_id!r} in {path}",
                            {"path": str(path), "index": index, "step_id": step_id},
                        )
                    )
                step_ids.add(step_id)
            repository_id = step.get("repository_id")
            if isinstance(repository_id, str) and repository_id not in scope_ids:
                findings.append(
                    Finding(
                        "GDS_PLAN_STEP_OUTSIDE_SCOPE",
                        "high",
                        f"Plan step {step_id!r} targets a repository outside scope",
                        {
                            "path": str(path),
                            "index": index,
                            "repository_id": repository_id,
                        },
                    )
                )
        canonical_plan = dict(instance)
        observed_digest = canonical_plan.pop("plan_digest", None)
        expected_digest = _sha256_json(canonical_plan)
        if observed_digest != expected_digest:
            findings.append(
                Finding(
                    "GDS_PLAN_DIGEST_MISMATCH",
                    "high",
                    f"Plan digest in {path} does not match its canonical payload",
                    {
                        "path": str(path),
                        "expected": expected_digest,
                        "observed": observed_digest,
                    },
                )
            )

    if schema_name == "rollout" and isinstance(instance, Mapping):
        wave_ids: set[str] = set()
        target_ids: list[str] = []
        for index, wave in enumerate(instance.get("waves", [])):
            if not isinstance(wave, Mapping):
                continue
            wave_id = wave.get("id")
            if isinstance(wave_id, str):
                if wave_id in wave_ids:
                    findings.append(
                        Finding(
                            "GDS_ROLLOUT_WAVE_DUPLICATE",
                            "high",
                            f"Rollout wave id {wave_id!r} occurs more than once in {path}",
                            {"path": str(path), "wave_id": wave_id},
                        )
                    )
                wave_ids.add(wave_id)
            if wave.get("ordinal") != index:
                findings.append(
                    Finding(
                        "GDS_ROLLOUT_WAVE_ORDER_INVALID",
                        "high",
                        f"Rollout wave ordinals in {path} are not contiguous",
                        {"path": str(path), "wave_id": wave_id},
                    )
                )
            target_ids.extend(
                repository_id
                for repository_id in wave.get("repository_ids", [])
                if isinstance(repository_id, str)
            )
        if len(target_ids) != len(set(target_ids)):
            findings.append(
                Finding(
                    "GDS_ROLLOUT_DUPLICATE_TARGET",
                    "high",
                    f"A repository occurs in multiple rollout waves in {path}",
                    {"path": str(path)},
                )
            )
        expected_target_digest = _sha256_json(sorted(target_ids))
        if (
            instance.get("target_count") != len(target_ids)
            or instance.get("target_set_digest") != expected_target_digest
        ):
            findings.append(
                Finding(
                    "GDS_ROLLOUT_TARGET_SET_MISMATCH",
                    "high",
                    f"Rollout target count or digest in {path} does not match its waves",
                    {
                        "path": str(path),
                        "expected_digest": expected_target_digest,
                    },
                )
            )
        canonical_rollout = dict(instance)
        observed_digest = canonical_rollout.pop("plan_digest", None)
        expected_digest = _sha256_json(canonical_rollout)
        if observed_digest != expected_digest:
            findings.append(
                Finding(
                    "GDS_ROLLOUT_PLAN_DIGEST_MISMATCH",
                    "high",
                    f"Rollout plan digest in {path} does not match its canonical payload",
                    {
                        "path": str(path),
                        "expected": expected_digest,
                        "observed": observed_digest,
                    },
                )
            )
    return findings


def _sha256_json(value: Any) -> str:
    payload = json.dumps(
        value, ensure_ascii=False, separators=(",", ":"), sort_keys=True
    ).encode("utf-8")
    return f"sha256:{hashlib.sha256(payload).hexdigest()}"


def _collect_leaf_pointers(value: Any, parts: list[str], result: set[str]) -> None:
    if isinstance(value, Mapping):
        for key, child in value.items():
            _collect_leaf_pointers(child, [*parts, str(key)], result)
        return
    if isinstance(value, list):
        for index, child in enumerate(value):
            _collect_leaf_pointers(child, [*parts, str(index)], result)
        return
    result.add(_json_pointer(parts))


def validate_instance(
    schema_set: SchemaSet, schema_name: str, path: Path
) -> list[Finding]:
    try:
        instance = load_data(path)
        validator = schema_set.validator(schema_name)
        errors = sorted(
            validator.iter_errors(instance),
            key=lambda error: (
                list(error.absolute_path),
                list(error.absolute_schema_path),
            ),
        )
    except InputContractError as exc:
        return [Finding(exc.code, "high", str(exc), {"path": str(path)})]
    except Unresolvable as exc:
        return [
            Finding(
                "GDS_SCHEMA_REFERENCE_UNRESOLVABLE",
                "high",
                f"Cannot resolve a schema reference for {path}: {exc}",
                {"path": str(path), "schema": schema_name},
            )
        ]

    findings = [
        Finding(
            "GDS_INSTANCE_INVALID",
            "high",
            f"{path} violates {schema_name} at "
            f"{_json_pointer(error.absolute_path)}: {error.message}",
            {
                "path": str(path),
                "schema": schema_name,
                "instance_path": _json_pointer(error.absolute_path),
                "schema_path": _json_pointer(error.absolute_schema_path),
                "validator": error.validator,
            },
        )
        for error in errors
    ]
    findings.extend(_semantic_findings(schema_name, instance, path))
    return findings


def validate_release_trust_identity(root: Path) -> list[Finding]:
    """Require consumer attestation identity to match the canonical provider."""

    anchor_path = root / ".gds" / "repository.yaml"
    trust_path = root / "requirements" / "bundle-trust.yaml"
    try:
        anchor = load_data(anchor_path)
        trust = load_data(trust_path)
    except InputContractError as exc:
        return [Finding(exc.code, "high", str(exc))]
    provider = anchor.get("provider") if isinstance(anchor, Mapping) else None
    source = trust.get("source") if isinstance(trust, Mapping) else None
    if not isinstance(provider, Mapping) or not isinstance(source, Mapping):
        return []  # Schema validation owns missing or malformed objects.
    expected = (provider.get("owner"), provider.get("name"))
    observed = (source.get("owner"), source.get("repository"))
    if expected == observed:
        return []
    return [
        Finding(
            "GDS_BUNDLE_TRUST_SOURCE_IDENTITY_MISMATCH",
            "high",
            "Bundle consumer trust source must match the control-plane provider identity",
            {
                "anchor_path": str(anchor_path),
                "trust_path": str(trust_path),
                "expected_owner": expected[0],
                "expected_repository": expected[1],
                "observed_owner": observed[0],
                "observed_repository": observed[1],
            },
        )
    ]


def validate_fixture_cases(schema_set: SchemaSet, cases_path: Path) -> list[Finding]:
    try:
        document = load_data(cases_path)
    except InputContractError as exc:
        return [Finding(exc.code, "high", str(exc), {"path": str(cases_path)})]

    if not isinstance(document, Mapping) or not isinstance(document.get("cases"), list):
        return [
            Finding(
                "GDS_FIXTURE_INDEX_INVALID",
                "high",
                f"Fixture index {cases_path} must contain a cases array",
                {"path": str(cases_path)},
            )
        ]

    findings: list[Finding] = []
    for index, case in enumerate(document["cases"]):
        if not isinstance(case, Mapping):
            findings.append(
                Finding(
                    "GDS_FIXTURE_CASE_INVALID",
                    "high",
                    f"Fixture case {index} must be an object",
                    {"path": str(cases_path), "index": index},
                )
            )
            continue

        case_id = case.get("id")
        schema_name = case.get("schema")
        relative_path = case.get("path")
        expected_valid = case.get("valid")
        expected_code = case.get("expected_code")
        if (
            not isinstance(case_id, str)
            or not isinstance(schema_name, str)
            or not isinstance(relative_path, str)
            or not isinstance(expected_valid, bool)
            or (not expected_valid and not isinstance(expected_code, str))
        ):
            findings.append(
                Finding(
                    "GDS_FIXTURE_CASE_INVALID",
                    "high",
                    f"Fixture case {index} has invalid metadata",
                    {"path": str(cases_path), "index": index},
                )
            )
            continue

        fixtures_root = cases_path.parent.resolve()
        fixture_path = (fixtures_root / relative_path).resolve()
        if not fixture_path.is_relative_to(fixtures_root):
            findings.append(
                Finding(
                    "GDS_FIXTURE_PATH_OUTSIDE_ROOT",
                    "high",
                    f"Fixture {case_id!r} escapes the fixture root",
                    {"case": case_id, "path": str(fixture_path)},
                )
            )
            continue
        case_findings = validate_instance(schema_set, schema_name, fixture_path)
        actual_valid = not case_findings
        codes = sorted({finding.code for finding in case_findings})
        expectation_met = actual_valid == expected_valid
        if not expected_valid and expected_code not in codes:
            expectation_met = False
        if not expectation_met:
            findings.append(
                Finding(
                    "GDS_FIXTURE_EXPECTATION_MISMATCH",
                    "high",
                    f"Fixture {case_id!r} did not meet its expected result",
                    {
                        "case": case_id,
                        "path": str(fixture_path),
                        "expected_valid": expected_valid,
                        "expected_code": expected_code,
                        "actual_valid": actual_valid,
                        "actual_codes": codes,
                    },
                )
            )
    return findings


def classify_exit(findings: Sequence[Finding]) -> tuple[str, int]:
    if not findings:
        return "success", EXIT_SUCCESS
    if any(finding.code == "GDS_INTERNAL_ERROR" for finding in findings):
        return "internal", EXIT_INTERNAL
    if any(
        finding.code.startswith(INPUT_FINDING_PREFIXES)
        or finding.code in INPUT_FINDING_CODES
        for finding in findings
    ):
        return "input", EXIT_INPUT
    return "validation", EXIT_VALIDATION


def result_envelope(findings: Sequence[Finding], command: str) -> dict[str, Any]:
    failed = bool(findings)
    exit_class, exit_code = classify_exit(findings)
    return {
        "schema_version": SCHEMA_VERSION,
        "command": command,
        "result": "failed" if failed else "succeeded",
        "exit_class": exit_class,
        "exit_code": exit_code,
        "scope": {},
        "findings": [finding.to_dict() for finding in findings],
        "mutation": {"attempted": False, "completed": False},
    }


def run_validation(root: Path, fixtures: Path | None = None) -> dict[str, Any]:
    schema_dir = root / "schemas" / "v1"
    schema_set, findings = load_schema_set(schema_dir)
    if schema_set is not None:
        findings.extend(
            validate_instance(schema_set, "repository", root / ".gds/repository.yaml")
        )
        findings.extend(
            validate_instance(
                schema_set,
                "migration-registry",
                root / "schemas/migrations/registry.yaml",
            )
        )
        findings.extend(
            validate_instance(
                schema_set,
                "skill-registry",
                root / "skills/registry.yaml",
            )
        )
        findings.extend(
            validate_instance(
                schema_set,
                "harness-registry",
                root / "harnesses/capability-registry.yaml",
            )
        )
        findings.extend(
            validate_instance(
                schema_set,
                "module-harness-bridge",
                root / "harnesses/module-bridge.yaml",
            )
        )
        for profile_path in sorted((root / "harnesses").glob("*/profile.yaml")):
            findings.extend(
                validate_instance(schema_set, "harness-profile", profile_path)
            )
        findings.extend(
            validate_instance(
                schema_set, "bundle-trust", root / "requirements/bundle-trust.yaml"
            )
        )
        findings.extend(validate_release_trust_identity(root))
        findings.extend(
            validate_instance(
                schema_set,
                "source-register",
                root / "docs/source-register/sources.yaml",
            )
        )
        for device_path in sorted((root / "estate" / "devices").glob("*.yaml")):
            findings.extend(validate_instance(schema_set, "device", device_path))
        if fixtures is not None:
            findings.extend(validate_fixture_cases(schema_set, fixtures))

    return result_envelope(findings, "gds validate schemas")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Validate GDS v1 schemas, canonical manifests, and fixtures."
    )
    parser.add_argument(
        "--root",
        type=Path,
        default=Path(__file__).resolve().parents[1],
        help="Control-plane repository root (default: inferred from this script).",
    )
    parser.add_argument(
        "--fixtures",
        type=Path,
        default=Path("tests/fixtures/schemas/v1/cases.json"),
        help="Fixture case index relative to --root; use an empty string to skip.",
    )
    parser.add_argument(
        "--json", action="store_true", help="Emit the JSON result envelope."
    )
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    root = args.root.expanduser().resolve()
    fixtures = args.fixtures
    if fixtures is not None and str(fixtures):
        fixtures = fixtures if fixtures.is_absolute() else root / fixtures
    else:
        fixtures = None

    try:
        if not root.is_dir():
            raise InputContractError(
                "GDS_ROOT_INVALID", f"Repository root does not exist: {root}"
            )
        result = run_validation(root, fixtures)
        exit_code = int(result["exit_code"])
    except InputContractError as exc:
        finding = Finding(exc.code, "high", str(exc))
        result = {
            **result_envelope([finding], "gds validate schemas"),
            "exit_class": "input",
            "exit_code": EXIT_INPUT,
        }
        exit_code = EXIT_INPUT
    except Exception as exc:  # final CLI boundary: never emit an unstructured traceback
        finding = Finding(
            "GDS_INTERNAL_ERROR",
            "critical",
            f"Unhandled schema validation error: {type(exc).__name__}: {exc}",
        )
        result = {
            **result_envelope([finding], "gds validate schemas"),
            "exit_class": "internal",
            "exit_code": EXIT_INTERNAL,
        }
        exit_code = EXIT_INTERNAL

    if args.json:
        print(json.dumps(result, indent=2, sort_keys=True))
    elif result["findings"]:
        for finding in result["findings"]:
            print(f"{finding['code']}: {finding['message']}", file=sys.stderr)
        print(f"GDS schema validation: FAILED ({len(result['findings'])} findings)")
    else:
        print("GDS schema validation: PASS")
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
