---
name: gds-drakkars-onboard-project
description: Use this skill only when the owner explicitly asks to connect one repository to Drakkars and the reusable CI system. Inventory its technologies, classify visibility, add the correct runner policy and complete checks, prove them on the repository's real jobs, and record estate evidence. Do not use it for multi-repository migration or interrupting existing runs.
---

# Contract

Onboard one project with complete CI coverage and no unsupported-tool surprises.

## Use when

- The owner explicitly approves end-to-end CI onboarding for one repository.

## Do not use when

- The repository only needs audit or optimization, or scope spans multiple consumers.

## Inputs

- Repository identity, visibility, required checks, release targets, and approved estate selector.

## Preconditions

1. Resolve every Git boundary and prove visibility before choosing runners or writing configuration.

## Workflow

1. Resolve repository identity, visibility, policy, required checks, and independent Git boundaries.
2. Detect languages, runtimes, package managers, lockfiles, services, containers, build tools, release targets, and architecture needs from source evidence.
3. Select public reusable workflow capabilities and keep private repository/priority/runner mappings in the estate overlay.
4. Add deterministic setup, validation, logs, telemetry, timeouts, fork safety, and artifacts for failures.
5. Validate workflow syntax and policy locally.
6. Let the repository's ordinary PR or push create the acceptance jobs. Do not
   dispatch synthetic, benchmark, soak, canary, rerun, or empty-commit traffic
   solely to qualify the path.
7. Promote only after those real required checks and their telemetry are
   complete; journal the exact commits and runtime evidence.

## Stop conditions

Stop on missing credentials, unknown visibility, unsupported required
technology, or a regression in real project jobs. Never cancel, supersede,
manually rerun, or terminate an existing run to make room.

## Output

Return inventory, runner mapping, coverage map, real-job result, evidence
locations, rollback path, and remaining gaps.

## Verification

Prove technology coverage, visibility-to-runner mapping, green real project
jobs, unchanged active jobs, and rollback evidence.

## References

Use source manifests and lockfiles, public reusable contracts, private estate policy, GitHub checks, and runtime telemetry.
