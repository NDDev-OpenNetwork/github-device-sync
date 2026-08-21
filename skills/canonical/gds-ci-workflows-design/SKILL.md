---
name: gds-ci-workflows-design
description: Use this skill when the owner wants a complete reusable CI/CD design for a project or ecosystem. Define coverage, job boundaries, dependencies, inputs, permissions, runner portability, technology setup, telemetry, and failure behavior. Do not use it to edit workflows, deploy runners, or onboard consumers.
---

# Contract

Design reusable CI that is complete, fast, explainable, and portable.

## Use when

- The owner needs a new or revised reusable CI architecture before implementation.

## Do not use when

- The request is to edit workflows, operate the fleet, onboard a project, or measure performance.

## Inputs

- Ecosystems, validation and release obligations, platforms, security policy, and consumer constraints.

## Preconditions

1. Inventory authoritative manifests, lockfiles, release processes, and policy; mark missing facts `NOT_PROVEN`.

## Workflow

1. Inventory validation obligations from source, manifests, lockfiles, release process, security policy, and supported platforms.
2. Group obligations into small jobs with explicit inputs, outputs, permissions, timeouts, and artifacts.
3. Express the minimum dependency DAG that preserves correctness; parallelize all independent jobs.
4. Specify deterministic, lockfile-aware setup for each detected ecosystem and version source.
5. Keep portable behavior and synthetic examples public. Expose runner selection as a consumer input; keep private identities and priorities in estate configuration.
6. Define logs, summaries, correlation fields, failure artifacts, retry classification, cache behavior, and observability degradation behavior.
7. Define hosted/public and private-fleet acceptance fixtures plus compatibility and security tests.

## Output

Return the coverage matrix, DAG, reusable interfaces, runner contract, security model, telemetry contract, acceptance tests, and rollout order.

## Stop conditions

Stop before repository edits, workflow dispatch, ruleset changes, or invented technology requirements.

## Verification

Every obligation has a job, every dependency is necessary, and no private identity occurs in portable design.

## References

Use current public reusable workflow contracts and synthetic public fixtures.
