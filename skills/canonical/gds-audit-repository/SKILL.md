---
name: gds-audit-repository
description: Use this skill when the owner asks to inspect one repository for GDS context, manifest, policy, projection, Git-state, or agent-system drift. Produce read-only evidence and remediation options. Do not use it to fix, synchronize, commit, push, merge, release, or clean anything.
---

# Contract

Audit one repository without changing local or external state.

## Use when

- The user asks for a repository health, context, or compliance review.
- A later mutation needs a verified baseline.
- Generated instructions, skills, or bundle state may have drifted.

## Do not use when

- The requested scope is the full estate.
- The user asks to apply a fix rather than report evidence.

## Inputs

- Repository path or stable repository ID.
- Requested audit depth.

## Preconditions

1. Resolve the repository with `gds context --json`.
2. Keep network refresh explicit; cached remote facts remain stale or unknown.

## Workflow

1. Run `gds status --json`.
2. Run `gds validate repository --json`.
3. Run applicable read-only validators, including projections and skills when
   present.
4. Classify findings by authority, severity, and proof state.
5. Separate confirmed defects from `NOT_PROVEN` evidence gaps.

## Stop conditions

Stop before any repair, provider write, Git integration, or cleanup action.

## Verification

Check that every finding names its object, evidence source, canonical owner,
and safe next action.

## Output

Return confirmed findings first, followed by risks, evidence gaps, and a
non-mutating remediation plan.

## References

No additional runtime reference is required; use current structured `gds` output.
