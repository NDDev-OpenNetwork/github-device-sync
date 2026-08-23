---
name: gds-onboard-repository
description: Use this skill only when the owner explicitly asks to add a brand-new repository to the GDS estate end to end. Walk the full onboarding sequence from provider creation through anchor, selector classification, device materialization, and projection regeneration. Do not use it for lifecycle changes to an existing repository; use gds-manage-repository instead.
---

# Contract

Onboard one new repository into the estate without leaving it orphaned,
unclassified, or unmaterialized on any target device.

## Use when

- A brand-new repository must join the estate and appear on target devices.

## Do not use when

- The repository already has a `.gds/repository.yaml` anchor (use
  `gds-manage-repository` for lifecycle changes).
- The task is source-code work inside an existing repository.

## Inputs

- Intended provider owner and repository name.
- Owning estate installation and (if mutable) mutation capability.
- Expected portfolio, visibility contract, and rollout ring.

## Preconditions

1. Confirm the provider repository exists and the estate installation can read
   it.
2. Confirm no existing repository ID or anchor collides with the target.
3. Resolve which selector will classify it and which devices should host it.

## Workflow

1. Onboard the anchor with `gds repository onboard --plan`, minting a stable
   `repo_` ID into a new `.gds/repository.yaml`. Carry provider identity,
   roles, classification, policy profiles, git integration, and rollout ring.
2. Confirm the selector classification: the observed provider repository must
   match exactly one highest-priority selector with no equal-priority tie. If
   no selector matches, verify the owner-default portfolio fallback is
   intended; if two selectors tie at the top, raise
   `GDS_ESTATE_SELECTOR_CONFLICT` and resolve the priority band before
   proceeding.
3. For each target device, add a materialization entry under
   `estate/devices/<device>.yaml` binding the selector's portfolio to a
   workspace root and mode (`active`/`reference`/`ephemeral`/`absent`).
4. Materialize the checkout with `gds workspace materialize --plan/--apply` on
   each target device, or defer to the phased bootstrap orchestrator.
5. Declare `product` in the new `.gds/repository.yaml`: what the repository is
   for, what it can do, and the `change → path` entrypoints. It is optional, and
   omitting it produces a brief that tells an agent what it may not do and
   nothing about the product — which is the state onboarding should not create.
6. Regenerate projections with `gds generate repository --plan/--apply` so
   `AGENTS.md`, `compiled-policy.json`, and `bundle.lock.yaml` reflect the new
   anchor.
7. Verify provider and local final state with the operation-specific verify
   commands.

## Stop conditions

Stop on identity ambiguity, selector conflict, inaccessible provider, dirty
unrelated work, stale plan, or insufficient approval for external writes.

## Verification

Confirm the new anchor resolves under `gds context --json`, the selector
classifies it without conflict, each target device hosts it at the intended
path, and regenerated projections are digest-consistent.

## Output

Return the stable repository ID, resolved portfolio and selector, per-device
placement, projection digests, and any deferred materialization state.

## References

Selector priority bands are `100` (generic source/fork), `200` (specialized
non-fork), and `300` (state override). See `docs/contracts/estate-v1.md` for
the precedence and conflict rules. The lifecycle mutation itself uses
`gds-manage-repository`; this skill owns the end-to-end sequence that wraps
it.
