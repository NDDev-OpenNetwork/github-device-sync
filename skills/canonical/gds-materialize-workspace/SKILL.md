---
name: gds-materialize-workspace
description: Use this skill only when the owner explicitly asks to clone or materialize a selected repository set on a device. Resolve selectors, local roots, clone modes, submodules, and resource limits before applying. Do not use it to clone the whole estate by default or synchronize existing checkouts.
---

# Contract

Materialize only the requested repository subset with verified identities and
bounded resources.

## Use when

- Selected repositories or a portfolio profile must become local checkouts.

## Do not use when

- Existing checkouts only need synchronization.
- No explicit repository selector or device root is available.
- The repository is owned outside the estate. Per ADR 0025 it is cloned by hand
  into `${HOME}/Developer/external` and has no selector, portfolio, or
  materialization assignment.
- The repository is consumed as a git submodule. Per ADR 0027 it exists only as
  its superproject's gitlink and is never given a standalone checkout. This is
  now enforced: plan and apply refuse with `GDS_WORKSPACE_EMBEDDED_ONLY` when the
  repository is the target of a resolved `git-submodule-consumer` relationship,
  and `gds workspace audit` reports an observed standalone copy as invalid. Role
  `module` alone does not trigger the refusal — a module with no consumer in
  this estate stays standalone-eligible.

## Inputs

- Device profile, repository selector, workspace root, and materialization mode.

## Preconditions

1. Discover existing checkouts and path conflicts.
2. Resolve provider identities and access without treating failures as absence.
3. Run `gds repository materialize --plan` per selected repository and obtain
   approval for network and filesystem writes.

## Workflow

1. Recheck target paths, free resources, access, and plan digest.
2. Materialize repositories with bounded concurrency and declared clone mode.
3. Initialize declared submodules or package state only when policy requires.
4. Validate repository anchors and register observed checkout state.

## Stop conditions

Stop on path collision, identity mismatch, inaccessible repository, partial clone
incompatibility, stale plan, resource limit, unexpected existing data, or
`GDS_WORKSPACE_EMBEDDED_ONLY`.

## Verification

Run `gds repository materialize --verify <operation-id> --json` and validate each
checkout identity.

## Output

Return materialized, skipped, failed, and pre-existing checkouts, exact paths,
clone modes, identities, and recovery steps.

## References

No additional runtime reference is required; use current device and repository identities.
