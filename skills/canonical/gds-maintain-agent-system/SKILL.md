---
name: gds-maintain-agent-system
description: "Use this skill only when the owner explicitly asks to update the GDS agent operating system itself: current source facts, capability profiles, canonical AGENTS templates, skills, schemas, generators, hooks, or policies. Validate, release immutably, and prepare canary rollout. Do not use it for ordinary repository feature work."
---

# Contract

Update one canonical GDS source, prove compatibility, and prepare controlled
distribution without direct estate-wide mutation.

## Use when

- Official harness behavior, GDS policy, skills, schemas, or generators changed.

## Do not use when

- The task is ordinary project implementation.
- The request only rolls out an already released bundle.

## Inputs

- Requested subsystem, current stable bundle, source register, and affected profiles.

## Preconditions

1. Open the source register and identify volatile affected claims.
2. Inspect current official sources and exact applicable versions.
3. Plan canonical changes, release gates, canaries, and rollback.

## Workflow

1. Change the canonical owner only.
2. Update ADRs, schemas, profiles, migrations, and source evidence as applicable.
3. Regenerate golden outputs.
4. Run static, security, skill, harness, migration, and reproducibility tests.
5. Build an immutable release candidate and prepare a canary rollout plan.

## Stop conditions

Stop on stale official evidence, unresolved authority conflict, security or
runtime regression, non-reproducible output, or missing rollback.

## Verification

Require all affected deterministic and behavioral release gates; runtime tests
that were not executed remain `NOT_PROVEN`.

## Output

Return sources inspected, canonical delta, tests, release candidate identity,
known limitations, canary plan, and external approvals still required.

## References

Read the affected official sources named by the control-plane source register.
