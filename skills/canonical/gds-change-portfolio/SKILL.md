---
name: gds-change-portfolio
description: Use this skill only when the owner explicitly asks to apply one logical change across repositories selected by a GDS portfolio. Create independent repository subplans, canaries, bounded waves, and an aggregate journal. Do not use it for one repository or pretend the portfolio has one Git transaction.
---

# Contract

Apply one coherent portfolio-wide change as bounded independent repository transactions.

## Use when

- A declared portfolio selector needs the same logical source or policy change.

## Do not use when

- The target is one repository.
- Repository-specific differences make one coherent change undefined.

## Inputs

- Portfolio selector, logical change, representative canaries, gates, and approval.

## Preconditions

1. Resolve exact repository identities, lifecycle, access, and applicable policies.
2. Build one aggregate plan with one subplan per Git boundary.
3. Obtain approval for the exact target set and external writes.

## Workflow

1. Apply representative canaries.
2. Verify repository-specific checks and aggregate failure thresholds.
3. Advance bounded waves without collapsing independent histories.
4. Pause on security, policy, or threshold failure.
5. Reconcile every target and preserve partial-completion state.

## Stop conditions

Stop on selector drift, ambiguous transformation, stale subplan, unexpected local
work, wave failure, changed provider state, or insufficient approval.

## Verification

Re-run `gds portfolio plan --json` and reconcile target counts. `gds portfolio`
plans only and has no apply or verify mode, so verify each repository subplan
with its own operation-specific `--verify <operation-id> --json`.

## Output

Return aggregate plan, per-repository commits/PRs/results, canary and wave evidence,
skips, failures, and safe resume or rollback state.

## References

No additional runtime reference is required; use the aggregate and repository subplans.
