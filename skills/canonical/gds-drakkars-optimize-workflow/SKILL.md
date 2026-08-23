---
name: gds-drakkars-optimize-workflow
description: Use this skill only when the owner explicitly asks to change a project's GitHub Actions for lower end-to-end latency while preserving or increasing coverage, stability, logs, and telemetry. Implement within one repository, verify locally, and prepare measured rollout evidence. Do not use it for fleet infrastructure changes or job cancellation.
---

# Contract

Optimize the full PR-to-deploy critical path without weakening required checks.

## Use when

- The owner explicitly requests an approved workflow optimization in one repository.

## Do not use when

- The request is audit-only, fleet/provider work, multi-consumer rollout, or would reduce coverage.

## Inputs

- Repository, approved scope, required checks, supported technologies, and baseline evidence.

## Preconditions

- Resolve GDS context and inspect repository instructions.
- Record a comparable baseline or mark it `NOT_PROVEN`.
- Preserve unrelated work and existing required-check identities unless migration is planned.

## Workflow

1. Derive the job DAG and locate the measured critical path.
2. Parallelize independent work; keep true dependencies explicit with `needs`.
3. Move reusable behavior to the pinned public CI workflow module when portable.
4. Select runners by visibility: public Linux hosted; private Linux private fleet; macOS/Windows hosted unless estate policy says otherwise.
5. Make setup deterministic for every detected ecosystem, including lockfile-aware caches and supported package managers.
6. Add bounded timeouts, useful summaries, correlation data, artifacts on failure, and safe retry classification.
7. Run syntax, policy, security, and project validations before any external write.

## Invariants

Do not skip tests to gain speed. Do not cancel active jobs. Do not put private facts in public reusable workflows.

## Output

Return changed files, preserved coverage, validation results, expected latency effect, and remaining measurement gaps.

## Stop conditions

Stop on lost coverage, unknown check identity, unsupported technology, unsafe cache or secret behavior, or unrelated dirty-state overlap.

## Verification

Show obligation-to-job coverage, validators, workflow policy checks, and a measurable latency hypothesis.

## References

Use current repository instructions, public reusable workflow contracts, and private runner policy.
