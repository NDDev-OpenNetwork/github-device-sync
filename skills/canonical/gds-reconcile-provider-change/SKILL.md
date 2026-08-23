---
name: gds-reconcile-provider-change
description: Use this skill only when the owner explicitly asks to reconcile the estate after a provider-side change to a repository, such as an owner-login case drift, a rename, a transfer, or an archive. Re-snapshot observed inventory, fix provider strings and anchors, re-resolve selectors, and regenerate projections. Do not use it to classify drift without fixing it; use gds-triage-estate-drift for read-only triage.
---

# Contract

Reconcile the estate to provider-side reality after an external repository
change, preserving stable identity where possible and reclassifying where
required.

## Use when

- A GitHub repository was renamed, transferred, archived, or its owner login
  changed case, and the estate must follow.
- Observed device inventory or anchors carry stale provider strings.

## Do not use when

- The owner only wants to see the drift, not fix it (use
  `gds-triage-estate-drift`).
- The change is local (branch, commit, worktree) rather than provider-side.

## Inputs

- Stable repository identity or provider locator of the affected repository.
- Nature of the provider change (rename, transfer, archive, login case).

## Preconditions

1. Confirm the provider change is real and complete (not mid-transfer).
2. Resolve the repository's stable ID and current anchor.
3. Identify every site holding a stale provider string: device inventory
   blocks, `.gds/repository.yaml` provider fields, and any typed relationship
   edges that name the old locator.

## Workflow

1. Re-snapshot observed inventory on each affected device with
   `gds inventory` so device descriptors carry current provider strings.
2. Update the repository anchor's provider fields (owner, name, numeric
   `repository_id`) via `gds repository rename` or `gds repository transfer`
   as appropriate, preserving the stable `repo_` ID.
3. If the change moved the repository across owners or portfolios, re-resolve
   selector classification and confirm no `GDS_ESTATE_SELECTOR_CONFLICT`
   arises from the new owner/portfolio combination.
4. If the repository was archived, confirm it now resolves to
   `portfolio:archived-projects` under the archived-precedence rule
   (priority `300`), regardless of prior fork or server classification.
5. Regenerate projections with `gds generate repository --plan/--apply` and
   refresh Serena memory digests if any memory source file changed.
6. Verify provider and local final state.

## Stop conditions

Stop on identity ambiguity, incomplete provider transfer, inaccessible
dependencies, stale plan, or insufficient approval for external writes.

## Verification

Confirm the anchor provider fields match the live provider, device inventory
blocks carry canonical owner/login case, selector classification is
unambiguous, and regenerated projections are digest-consistent.

## Output

Return the stable repository ID, old and new provider locators, reclassification
evidence (if any), updated device inventory paths, and projection digests.

## References

Owner login is matched case-insensitively by the estate compiler but should be
stored in canonical form everywhere. The archived-precedence rule and priority
bands are documented in `docs/contracts/estate-v1.md`. Read-only drift triage
that should precede this skill is owned by `gds-triage-estate-drift`.
