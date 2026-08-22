---
name: gds-manage-repository
description: Use this skill only when the owner explicitly asks to create, onboard, rename, transfer, archive, rehome, or otherwise change the lifecycle of a Git repository under GDS. Preserve stable identity and plan every provider and local mutation. Do not use it for source-code work or repository deletion hidden as removal.
---

# Contract

Change repository lifecycle or provider location without losing identity,
relationships, evidence, or unrelated work.

## Use when

- A repository must be onboarded or its lifecycle/provider locator changed.

## Do not use when

- The task only changes repository code.
- Deletion was not requested explicitly by exact identity.

## Inputs

- Stable repository identity or proposed new identity.
- Requested lifecycle operation and exact provider/local scope.

## Preconditions

1. Inspect provider and local evidence.
2. Resolve dependents, forks, modules, workflows, and access boundaries.
3. Run the specific `gds repository <operation> --plan` command.
4. Obtain approval for exact external writes and destructive steps.

## Workflow

1. Recheck identity, access, policy, and plan preconditions.
2. Apply one idempotent plan step at a time.
3. Preserve aliases and locator history across rename or transfer.
4. Regenerate affected projections and reconcile relationships.
5. Verify provider and local final state.

## Stop conditions

Stop on identity ambiguity, inaccessible dependencies, dirty unrelated work,
changed provider state, stale plan, or insufficient approval.

## Verification

Run the operation-specific `gds repository ... --verify <operation-id> --json`.

## Output

Return stable identity, old and new locators, applied steps, affected
relationships, verification evidence, and recovery state.

## References

No additional runtime reference is required; use typed plan and verification output.
