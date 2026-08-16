---
name: gds-handoff-work
description: Use this skill only when the owner wants to preserve unfinished work for another device or session. Plan an exact checkpoint commit, push the task branch, and create or update a draft pull request when policy requires it. Do not use it to merge, complete, synchronize main, or clean branches and worktrees.
disable-model-invocation: true
---

# Contract

Preserve unfinished work without integrating or deleting it.

## Use when

- The owner explicitly asks to hand off unfinished work.
- Local task work must become verifiably available on another device.

## Do not use when

- The work should be fully completed and integrated.
- The request is only for status, synchronization, or backup by stash.

## Inputs

- Current repository and task branch.
- Approved file set and checkpoint message.
- Repository handoff and draft-PR policy.

## Preconditions

1. Run `gds context --json`.
2. Run `gds handoff --plan --json` for the current repository scope.
3. Present staged, unstaged, untracked, sensitive, test, upstream, and remote
   evidence.
4. Obtain explicit approval for the exact commit, push, and optional draft PR.

## Workflow

1. Recheck plan preconditions.
2. Apply only the approved plan.
3. Verify the remote branch OID and draft PR state when applicable.
4. Leave the task branch and worktree intact.

## Stop conditions

Stop on protected default branch, conflicts, unapproved files, secret risk,
failed checkpoint checks, changed remote OID, unavailable authorization, stale
plan, or unresolved policy. If the command is unavailable, report `NOT_PROVEN`.

## Verification

Run `gds handoff --verify <operation-id> --json`.

## Output

Return repository ID, local and verified remote OIDs, draft PR state, checks,
remaining local changes, and the exact continuation instruction.

## References

No additional runtime reference is required; the generated plan is authoritative.
