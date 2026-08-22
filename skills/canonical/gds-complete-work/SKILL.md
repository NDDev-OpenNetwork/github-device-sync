---
name: gds-complete-work
description: "Use this skill only when the owner explicitly asks to finish the current work completely across every affected Git repository: implement, verify, integrate, publish, update dependency pins, and remove only proven-safe completed branches or worktrees. Do not use it for status, routine sync, or unfinished handoff."
---

# Contract

Complete one approved unit of work across all affected Git boundaries while
preserving unrelated work.

## Use when

- The owner explicitly asks to finish, integrate, publish, and safely clean.
- Module and consumer changes must be finalized in dependency order.

## Do not use when

- Work is unfinished or only needs cross-device preservation.
- Merge, publication, or cleanup authorization is absent.

## Inputs

- Task identity and affected repository graph.
- Repository verification, integration, release, and cleanup policies.
- Exact approval scope.

## Preconditions

1. Resolve every affected Git boundary.
2. Run `gds complete --plan --task-id <task-id> --json`.
3. Verify dependency order, checks, reviews, permissions, and release policy.
4. Obtain approval for every external write and cleanup action.

## Workflow

1. Finish implementation and role-specific verification.
2. Finalize and publish dependencies before consumers.
3. Update consumer pins and rerun consumer verification.
4. Integrate and push according to repository policy.
5. Merge the pull request once its required checks pass, and delete the head
   branch in the same action. Per ADR 0030 no estate repository requires a human
   approving review, so the owner's agent merges its own pull request. Required
   status checks stay the gate: never reach for `--admin` to move past a failing
   or unreported required check — that is a configuration bug to fix at its
   source.
6. Remove only branches and worktrees proven safe by the approved plan. A merged
   head branch is always safe and is deleted immediately; leaving it is drift.

## Stop conditions

Stop on unknown access, unrelated dirty work, unpublished dependency commits,
changed OIDs, failing or unknown required checks, an unsatisfiable branch rule,
stale plan, or unsafe cleanup targets. If the command is unavailable, report `NOT_PROVEN`.

## Verification

Run `gds complete --verify <operation-id> --json`.

## Output

Return final refs, checks, dependency pins, integrations, cleanup evidence,
remaining work, and any partial-completion journal state.

## References

No additional runtime reference is required; the generated plan is authoritative.
