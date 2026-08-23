---
name: gds-manage-fork
description: Use this skill only when the owner explicitly asks to create, inspect, synchronize, rehome, detach, freeze, archive, or otherwise change a fork lifecycle. Preserve fork-specific commits and upstream identity; force is never the default. Do not use it for ordinary source repositories or generic branch synchronization.
---

# Contract

Manage a fork relationship without discarding maintained fork history.

## Use when

- A repository fork must be synchronized or its fork policy changed.

## Do not use when

- The repository is not verified as a fork.
- The task is ordinary feature development.

## Inputs

- Fork repository ID, upstream identity, branch, and fork policy.
- Requested operation and explicit approval scope.

## Preconditions

1. Verify `origin` and `upstream` identities, not only their names.
2. Classify fork-specific and upstream commits.
3. Run `gds fork <operation> --plan` for `sync`, `detach`, or `archive`;
   `gds fork inspect` is read-only and has no plan mode.
4. For any force proposal, require exact old OID, recovery ref, and explicit approval.

## Workflow

1. Recheck remote OIDs and access.
2. Apply the approved integration strategy.
3. Preserve upstream history and fork-specific commit evidence.
4. Verify final refs, policy, and relationships.

## Stop conditions

Stop on inaccessible upstream, ambiguous commit ownership, changed OIDs,
missing recovery path, stale plan, or unapproved force behavior.

## Verification

Run `gds fork <operation> --verify <operation-id> --json` for the mutating
operation, then `gds fork inspect --json` for final identity evidence.

## Output

Return fork/upstream identities, classified deltas, applied strategy, final
OIDs, preserved commits, and unresolved access state.

## References

No additional runtime reference is required; use current fork and upstream evidence.
