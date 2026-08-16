---
name: gds-sync-checkouts
description: Use this skill only when the owner explicitly asks to apply safe synchronization to selected existing local checkouts. Refresh remote evidence, skip dirty or diverged boundaries, and apply only approved fast-forward or policy-defined actions. Do not use it at session start or to clone, rebase, force, merge, or clean broadly.
disable-model-invocation: true
---

# Contract

Synchronize selected existing checkouts without overwriting active or unrelated
work.

## Use when

- The owner explicitly requests safe local synchronization.

## Do not use when

- The request is session orientation or status only.
- Missing repositories should be cloned.

## Inputs

- Device and repository selector.
- Allowed refresh and integration policy.

## Preconditions

1. Inspect every selected worktree, branch, upstream, remote, and lock.
2. Refresh decision-relevant remotes explicitly and record old/new OIDs.
3. Run `gds sync checkouts --plan` and obtain approval for local mutations.

## Workflow

1. Recheck worktree fingerprints, remote OIDs, and locks.
2. Apply only plan-approved safe actions per repository.
3. Skip dirty, diverged, conflicted, detached-ambiguous, or policy-unknown states.
4. Verify each resulting branch and worktree.

## Stop conditions

Stop or skip on unrelated changes, divergence, force update, stale plan,
authentication failure, unknown remote freshness, or required integration
beyond policy.

## Verification

Run `gds sync checkouts --verify <operation-id> --json`.

## Output

Return updated, unchanged, skipped, blocked, and failed checkouts with before/after
OIDs and no claim that skipped repositories are synchronized.

## References

No additional runtime reference is required; use the generated synchronization plan.
