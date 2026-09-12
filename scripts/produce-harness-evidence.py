#!/usr/bin/env python3
"""Produce public, signed active-seven evidence from fresh exact setup-system runs."""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import re
import stat
import subprocess
import tempfile
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

ACTIVE = (
    "antigravity", "claude-code", "codex", "cursor",
    "grok-build", "opencode", "pi",
)
COMMIT = re.compile(r"^[0-9a-f]{40}$")
VERSION = re.compile(r"^[0-9A-Za-z][0-9A-Za-z._+-]{0,127}$")


class EvidenceError(RuntimeError):
    pass


def compact(value: Any, *, sorted_keys: bool = False) -> bytes:
    return json.dumps(
        value, ensure_ascii=False, separators=(",", ":"), sort_keys=sorted_keys,
    ).encode()


def digest(value: Any) -> str:
    # GDS verifies these typed payloads after decoding them into Go structs.
    # encoding/json emits struct fields in declaration order, which is the
    # insertion order used when each payload is built below. Sorting keys here
    # would produce a different detached digest even though the signature over
    # the typed payload remains valid.
    return "sha256:" + hashlib.sha256(compact(value)).hexdigest()


def bytes_digest(raw: bytes) -> str:
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def gh_json(*arguments: str) -> Any:
    done = subprocess.run(
        ["gh", "api", *arguments], capture_output=True, text=True, check=False,
    )
    if done.returncode != 0:
        raise EvidenceError(f"GitHub observation failed for {arguments[0]}: {done.stderr.strip()}")
    try:
        return json.loads(done.stdout)
    except json.JSONDecodeError as exc:
        raise EvidenceError(f"GitHub returned invalid JSON for {arguments[0]}") from exc


def repository_file(repository: str, path: str, commit: str) -> bytes:
    document = gh_json(f"repos/{repository}/contents/{path}?ref={commit}")
    if document.get("type") != "file" or document.get("encoding") != "base64":
        raise EvidenceError(f"{repository}:{path} is not a base64 file")
    try:
        return base64.b64decode(document["content"], validate=False)
    except (KeyError, ValueError) as exc:
        raise EvidenceError(f"{repository}:{path} content is invalid") from exc


def nested(document: Any, path: list[str]) -> Any:
    current = document
    for component in path:
        if not isinstance(current, dict) or component not in current:
            raise EvidenceError(f"version path {'.'.join(path)} is absent")
        current = current[component]
    return current


def timestamp(value: Any) -> datetime:
    if not isinstance(value, str):
        raise EvidenceError("evidence timestamp is absent")
    try:
        result = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise EvidenceError("evidence timestamp is invalid") from exc
    if result.tzinfo is None:
        raise EvidenceError("evidence timestamp must have a timezone")
    return result.astimezone(timezone.utc)


def require_public_repository(repository: str) -> None:
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise EvidenceError("repository identity is invalid")
    value = gh_json(f"repos/{repository}")
    if value.get("private") is not False or value.get("full_name", "").lower() != repository.lower():
        raise EvidenceError("public evidence requires an exactly identified public repository")


def observe_harness(item: dict[str, Any], *, now: datetime | None = None) -> dict[str, Any]:
    now = now or datetime.now(timezone.utc)
    repository = item["repository"]
    require_public_repository(repository)
    commit = gh_json(f"repos/{repository}/commits/main").get("sha", "")
    if not COMMIT.fullmatch(commit):
        raise EvidenceError(f"{repository} main commit is invalid")
    runs = gh_json(
        f"repos/{repository}/actions/workflows/evidence.yml/runs?branch=main&per_page=20"
    ).get("workflow_runs", [])
    run = next(
        (candidate for candidate in runs if candidate.get("head_sha") == commit
         and candidate.get("status") == "completed" and candidate.get("conclusion") == "success"),
        None,
    )
    if run is None:
        raise EvidenceError(f"{repository}@{commit} has no successful evidence.yml run")
    attempt = run.get("run_attempt")
    if type(attempt) is not int or attempt < 1:
        raise EvidenceError("successful run has no exact attempt identity")
    jobs = gh_json(
        f"repos/{repository}/actions/runs/{run['id']}/attempts/{attempt}/jobs?per_page=100"
    ).get("jobs", [])
    expected_jobs = {f"lifecycle ({platform})" for platform in (
        "linux/x86_64", "linux/arm64", "macos/x86_64", "macos/arm64",
        "windows/x86_64", "windows/arm64",
    )}
    if len(jobs) != len(expected_jobs) or {job.get("name") for job in jobs} != expected_jobs:
        raise EvidenceError("evidence matrix is not the exact unique platform set")
    completed = []
    job_ids = set()
    jobs = sorted(jobs, key=lambda job: job["name"])
    for job in jobs:
        ident = job.get("id")
        if type(ident) is not int or ident < 1 or ident in job_ids:
            raise EvidenceError("evidence jobs lack unique numeric identities")
        job_ids.add(ident)
        if (job.get("status") != "completed" or job.get("conclusion") != "success"
                or job.get("run_id") != run["id"] or job.get("head_sha") != commit):
            raise EvidenceError("evidence job does not prove the exact successful run and source")
        finish = timestamp(job.get("completed_at"))
        if not timedelta(0) <= now - finish < timedelta(hours=72):
            raise EvidenceError("evidence job is stale or dated in the future")
        completed.append(finish)
    oldest_completion = min(completed).replace(microsecond=0)
    observed_jobs = sorted((job["name"], job["conclusion"]) for job in jobs)
    baseline_raw = repository_file(repository, item["baseline"], commit)
    baseline = json.loads(baseline_raw)
    version = nested(baseline, item["version_path"])
    if not isinstance(version, str) or not VERSION.fullmatch(version):
        raise EvidenceError(f"{repository} executable version is unsafe: {version!r}")
    workflow_raw = repository_file(repository, ".github/workflows/evidence.yml", commit)
    script_raw = repository_file(repository, "scripts/evidence.py", commit)
    cases = {
        "module_sha": commit,
        "run_id": run["id"], "run_attempt": attempt,
        "jobs": [{"id": job["id"], "name": job["name"], "conclusion": job["conclusion"],
                  "completed_at": job["completed_at"]} for job in jobs],
        "baseline_digest": bytes_digest(baseline_raw),
        "workflow_digest": bytes_digest(workflow_raw),
        "script_digest": bytes_digest(script_raw),
    }
    return {
        "harness_id": item["id"], "repository": repository,
        "module_sha": commit, "executable_version": version,
        "run_id": run["id"], "run_attempt": attempt, "run_url": run["html_url"],
        "runtime_observed_at": oldest_completion.isoformat().replace("+00:00", "Z"),
        "suite_cases_digest": digest(cases), "jobs": observed_jobs,
    }


def sign(private_key: Path, domain: str, payload: dict[str, Any]) -> str:
    message = domain.encode() + b"\n" + compact(payload)
    with tempfile.NamedTemporaryFile() as source, tempfile.NamedTemporaryFile() as signature:
        source.write(message)
        source.flush()
        done = subprocess.run(
            ["openssl", "pkeyutl", "-sign", "-rawin", "-inkey", str(private_key),
             "-in", source.name, "-out", signature.name],
            capture_output=True, text=True, check=False,
        )
        if done.returncode != 0:
            raise EvidenceError(f"Ed25519 signing failed: {done.stderr.strip()}")
        raw = Path(signature.name).read_bytes()
    if len(raw) != 64:
        raise EvidenceError("Ed25519 signature length is invalid")
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def public_key(private_key: Path) -> str:
    done = subprocess.run(
        ["openssl", "pkey", "-in", str(private_key), "-pubout", "-outform", "DER"],
        capture_output=True, check=False,
    )
    if done.returncode != 0 or len(done.stdout) < 32:
        raise EvidenceError("cannot derive Ed25519 public key")
    raw = done.stdout[-32:]
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def write_json(path: Path, value: Any, mode: int = 0o600) -> None:
    path.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    path.chmod(mode)


def select_signer(policy: dict[str, Any], private_key: Path, now: datetime,
                  actor_id: str | None = None, key_id: str | None = None) -> tuple[dict, dict]:
    derived = public_key(private_key)
    matches = []
    for identity in policy.get("identities", []):
        if actor_id is not None and identity.get("actor_id") != actor_id:
            continue
        if not {"harness-evidence", "harness-evidence-aggregate"} <= set(identity.get("roles", [])):
            continue
        for key in identity.get("keys", []):
            if key_id is not None and key.get("key_id") != key_id:
                continue
            if (key.get("algorithm") == "ed25519" and key.get("status") == "active"
                    and key.get("public_key") == derived
                    and timestamp(key.get("valid_from")) <= now < timestamp(key.get("valid_until"))):
                matches.append((identity, key))
    if len(matches) != 1:
        raise EvidenceError("signing key has no unique active identity with both evidence roles")
    return matches[0]


def evidence_expiry(observations: list[dict[str, Any]], key: dict[str, Any], now: datetime) -> datetime:
    runtime_times = [timestamp(item["runtime_observed_at"]) for item in observations]
    if not runtime_times or any(value > now for value in runtime_times):
        raise EvidenceError("runtime observation times are absent or in the future")
    expires = min(now + timedelta(hours=48), timestamp(key["valid_until"]),
                  *(value + timedelta(hours=72) for value in runtime_times))
    if expires <= now:
        raise EvidenceError("runtime evidence or signer is already expired")
    return expires


def producer_identity(config: dict[str, Any], root: Path) -> str:
    producer = config.get("producer", {})
    repository = producer.get("repository", "")
    ref = producer.get("ref", "")
    require_public_repository(repository)
    if not re.fullmatch(r"refs/(heads|tags)/[A-Za-z0-9._/-]+", ref):
        raise EvidenceError("producer ref is invalid")
    def git(*args: str) -> str:
        return subprocess.run(["git", "-C", str(root), *args], capture_output=True,
                              text=True, check=True).stdout.strip()
    head = git("rev-parse", "HEAD")
    if not COMMIT.fullmatch(head) or git("rev-parse", "--verify", ref + "^{commit}") != head:
        raise EvidenceError("producer checkout does not match its declared ref")
    if git("status", "--porcelain", "--untracked-files=all"):
        raise EvidenceError("producer source must be committed and clean")
    remote = git("remote", "get-url", "origin").removesuffix(".git")
    if remote not in (f"https://github.com/{repository}", f"git@github.com:{repository}"):
        raise EvidenceError("producer source repository differs from declared public identity")
    published = gh_json(f"repos/{repository}/commits/{head}")
    if published.get("sha") != head:
        raise EvidenceError("producer commit is not publicly published")
    return head


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--gds-root", type=Path, required=True)
    parser.add_argument("--private-key", type=Path, required=True)
    parser.add_argument("--signer-policy", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--channel", choices=("canary", "stable", "frozen"), default="stable")
    parser.add_argument("--actor-id")
    parser.add_argument("--key-id")
    args = parser.parse_args()

    key_info = args.private_key.lstat()
    if not stat.S_ISREG(key_info.st_mode) or stat.S_IMODE(key_info.st_mode) & 0o077:
        raise EvidenceError("private key must be a regular file with no group/other permissions")
    if args.output.exists():
        raise EvidenceError("output path already exists")
    config = json.loads(args.config.read_text(encoding="utf-8"))
    harnesses = config.get("harnesses", [])
    if config.get("schema_version") != 1 or tuple(item.get("id") for item in harnesses) != ACTIVE:
        raise EvidenceError("producer config is not the exact ordered active-seven set")
    gds_root = args.gds_root.resolve(strict=True)
    # Producer identity belongs to the public implementation, never the
    # private caller's current directory or estate commit.
    producer_root = Path(__file__).resolve().parents[1]
    head = producer_identity(config, producer_root)
    now = datetime.now(timezone.utc).replace(microsecond=0)
    policy = json.loads(args.signer_policy.read_text(encoding="utf-8"))
    if policy.get("schema_version") != 1:
        raise EvidenceError("signer policy schema is invalid")
    identity, key = select_signer(policy, args.private_key, now, args.actor_id, args.key_id)
    args.actor_id, args.key_id = identity["actor_id"], key["key_id"]
    observations = [observe_harness(item, now=now) for item in harnesses]
    expires = evidence_expiry(observations, key, now)
    bridge_digest = bytes_digest((gds_root / "harnesses/module-bridge.yaml").read_bytes())
    args.output.mkdir(mode=0o700)
    records_dir = args.output / "records"
    records_dir.mkdir(mode=0o700)
    records = []
    entries = []
    for observed in observations:
        harness_id = observed["harness_id"]
        profile_digest = bytes_digest((gds_root / f"harnesses/{harness_id}/profile.yaml").read_bytes())
        payload = {
            "schema_version": 1,
            "evidence_id": f"evidence-{harness_id}-{observed['run_id']}",
            "harness_id": harness_id,
            "harness_root_sha": head,
            "module_sha": observed["module_sha"],
            "profile_digest": profile_digest,
            "bridge_digest": bridge_digest,
            "executable_version": observed["executable_version"],
            "platform": {"os": "multi", "architecture": "multi", "device_class": "github-hosted"},
            "suite_version": config["suite_version"],
            "suite_cases_digest": observed["suite_cases_digest"],
            "result": "pass",
            "generated_at": now.isoformat().replace("+00:00", "Z"),
            "expires_at": expires.isoformat().replace("+00:00", "Z"),
            "actor_id": args.actor_id,
        }
        evidence_digest = digest(payload)
        record = {
            "payload": payload, "evidence_digest": evidence_digest,
            "signature": {"algorithm": "ed25519", "key_id": args.key_id,
                          "value": sign(args.private_key, "gds-harness-runtime-evidence/v1", payload)},
        }
        write_json(records_dir / f"{harness_id}.json", record)
        records.append(record)
        entries.append({"harness_id": harness_id, "evidence_digest": evidence_digest})
    manifest_payload = {
        "schema_version": 1, "manifest_id": f"manifest-{head[:12]}-{int(now.timestamp())}",
        "harness_root_sha": head, "channel": args.channel,
        "generated_at": now.isoformat().replace("+00:00", "Z"),
        "expires_at": expires.isoformat().replace("+00:00", "Z"),
        "actor_id": args.actor_id, "evidence": entries,
    }
    manifest = {
        "payload": manifest_payload, "manifest_digest": digest(manifest_payload),
        "signature": {"algorithm": "ed25519", "key_id": args.key_id,
                      "value": sign(args.private_key, "gds-harness-runtime-manifest/v1", manifest_payload)},
    }
    write_json(records_dir / "manifest.json", manifest)
    # Keep independent signer roles, key status and validity exactly as supplied.
    # Only the public producer/module anchors are attached by this observation.
    policy["harness_evidence"] = {
        "producer": {**config["producer"], "commit": head},
        "modules": {item["harness_id"]: item["module_sha"] for item in observations},
    }
    write_json(args.output / "trust-policy.json", policy)
    write_json(args.output / "observation.json", {
        "schema_version": 1, "producer_commit": head, "observed_at": now.isoformat().replace("+00:00", "Z"),
        "harnesses": observations,
    })
    print(json.dumps({"result": "succeeded", "records": len(records),
                      "manifest_digest": manifest["manifest_digest"], "output": str(args.output)}))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except EvidenceError as exc:
        print(json.dumps({"result": "failed", "error": str(exc)}), file=os.sys.stderr)
        raise SystemExit(2)
