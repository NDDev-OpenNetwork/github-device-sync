---
name: gds-manage-module
description: Use this skill only when the owner explicitly asks to add, replace, reclassify, or remove a module relationship for a project. Validate independent Git boundaries, typed relationships, .gitmodules or package metadata, visibility, and pin policy. Do not use it for ordinary code changes inside an existing module.
---

# Contract

Change a project-to-module relationship without conflating repository identity,
filesystem location, consumption mode, or release policy.

## Use when

- A module dependency relationship must be created, replaced, or removed.

## Do not use when

- Only module source code changes.
- The request is to release or update consumers after a release.

## Inputs

- Consumer and module repository IDs.
- Consumption, pin, publication, path, and visibility policies.

## Preconditions

1. Resolve both independent Git boundaries.
2. Validate module identity, access, and public/private context flow.
3. Run `gds module <operation> --plan` and obtain exact approval.

## Workflow

1. Recheck relationship and provider state.
2. Apply the approved repository and manifest changes.
3. Validate `.gitmodules`, gitlink, package, or service metadata as applicable.
4. Run consumer verification and regenerate derived context.

## Stop conditions

Stop on identity ambiguity, path collision, private-context leak, unpublished
pin, incompatible policy, unrelated dirty work, stale plan, or
`GDS_IDENTITY_CONSUMPTION_UNDECLARED`.

## Verification

Run the operation-specific `gds module ... --verify <operation-id> --json`.

## Output

Return typed relationship, source and target identities, consumption and pin
policy, changed files, checks, and remaining consumer actions.

## References

A module contract must enumerate every mechanism its consumers actually use.
`GDS_IDENTITY_CONSUMPTION_UNDECLARED` fires when a resolved dependency's
`module.consumption` omits the mode its consumer relies on: `git-submodule` for a
gitlink, `runtime-service` for a GitHub reusable workflow consumed through
`workflow-module-consumer`. A module both pinned and executed as a workflow
declares both. Adding a `git-submodule-consumer` relationship without updating the
dependency's own anchor therefore fails, and the fix belongs in the dependency
repository, not the consumer.

Per ADR 0027, a submodule-consumed repository is materialized only as the
superproject's gitlink and never as a standalone checkout. Embedded placement
requires role `superproject` on the superproject and role `module` on the
module; declaring role `module` obliges a `module` block in
`.gds/repository.yaml`, enforced by `schemas/v1/repository.schema.json`, so add
both in one change or validation fails with `GDS_INSTANCE_INVALID`. Never delete
a redundant standalone checkout before its commits are preserved into the
submodule's own Git store. Otherwise no additional runtime reference is
required; use current typed relationship policy.
