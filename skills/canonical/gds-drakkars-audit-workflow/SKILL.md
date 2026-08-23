---
name: gds-drakkars-audit-workflow
description: Use this skill for a read-only review of GitHub Actions coverage, dependencies, parallelism, runner selection, permissions, caching, reproducibility, observability, and latency. Check that public and private repositories use the correct runner authority. Do not use it to edit workflows or change GitHub settings.
---

# Contract

Audit one project's CI/CD behavior without reducing verification scope or mutating state.

## Use when

- One repository's workflows need a correctness, coverage, safety, or performance audit.

## Do not use when

- The owner asks to implement changes, audit fleet runtime, or trace only one run.

## Inputs

- Repository path, workflow scope, required checks, and supported release targets.

## Preconditions

1. Resolve repository visibility, instructions, and required-check authority.

## Workflow

1. Inventory workflows, triggers, required checks, reusable calls, matrices, environments, artifacts, caches, and deployment gates.
2. Map project languages, package managers, generated assets, security obligations, builds, tests, and release paths to actual jobs.
3. Verify public Linux jobs use GitHub-hosted resources and private Linux jobs
   use the declared private fleet, including private repositories in free
   organizations; keep macOS and Windows hosted unless private capacity is
   explicitly declared.
4. Build the dependency graph and critical path. Find accidental serialization,
   duplicated setup and security placements, oversized matrices, unsafe cache
   keys, and concurrency groups that can discard queued or running evidence.
5. Verify least-privilege permissions, pinning, secret isolation, fork safety, timeouts, concurrency, retry ownership, logs, telemetry, and artifact retention.
6. Report missing coverage separately from speed opportunities.

## Output

Return coverage gaps, correctness risks, critical-path evidence, safe parallelization opportunities, and an ordered change plan.

## Stop conditions

Stop before editing YAML, changing rulesets, dispatching workflows, or changing runners.

## Verification

Prove each finding from workflow source, manifests, policy, and required-check state.

## References

Use current workflow source, repository manifests, public reusable contracts, and private estate policy.
