# ADR 0025: Keep third-party collaboration checkouts in an out-of-estate external root

Status: Accepted

Superseded in part by: ADR 0026 (the owner-specific `servers` roots named below)

Date: 2026-07-26

## Context

ADR 0018 established that device checkout placement is declared by typed
assignments in `estate/devices/*.yaml`, where a portfolio selector resolves to
one named `workspace_root`. Every selector in `estate/selectors/` matches
`owner:example-user` or `owner:nddev`, and both installations in
`estate/installations/` are scoped to those two accounts.

A repository owned by a third-party GitHub account therefore cannot be
classified, discovered, or observed by this estate, even though work on it is
legitimate and routine — a collaborator checkout where the owner has write
access is a normal case, not an anomaly.

Two placements were considered and rejected:

- Placing it under an estate-managed root such as the `example-user`
  (`portfolio:personal-projects`) root asserts a portfolio the repository can
  never match, producing permanent classification drift.
- Forking it into the `example-user-forks` root changes the collaboration model.
  The fork portfolio exists for upstreams without write access; forking a
  private repository the owner can already push to adds cross-fork pull-request
  friction and leaves the counterparty unable to push.

Adding an `external` entry to `workspace_roots` was also rejected. Workspace
roots are read only through `materialization.include[].workspace_root`, so a
root no selector can reference is unreachable configuration that falsely
implies GDS materializes there.

## Decision

1. Checkouts of repositories owned by accounts outside the estate live under the
   device-local root `${HOME}/Developer/external`.
2. That root is a convention for humans and agents only. It is deliberately not
   declared in any device descriptor's `workspace_roots`, holds no portfolio
   assignment, and is never a materialization target.
3. Such checkouts carry no `.gds/repository.yaml` anchor. Anchoring a repository
   owned by another party would commit estate governance artifacts into a tree
   this estate does not own.
4. `gds workspace audit` raises exactly one expected finding per external
   checkout, `GDS_WORKSPACE_ANCHOR_REQUIRED` with `anchor_state: missing`. This
   is the accepted steady state, not drift to remediate. Because the root is
   outside every declared `workspace_root`, no placement finding
   (`GDS_WORKSPACE_PLACEMENT_DRIFT`, `GDS_WORKSPACE_ROOT_NOT_READY`) is produced
   and no bogus `expected_path` is computed.
5. Promotion out of `external` is an explicit estate change: it requires a new
   owner, an installation that can observe the account, and a selector — that
   is, transfer or adoption of the repository, never a local move alone.

## Consequences

- Third-party collaboration has one predictable device location, distinct from
  the four estate-managed root families (`control-plane`, `nddev`, `example-user`,
  and the owner-specific `forks` roots; **ADR 0026 superseded the
  owner-specific `servers` roots with one flat `servers` root**).
- Estate audits stay truthful: an external checkout is reported as unanchored
  rather than silently absent or wrongly classified.
- The anchor finding is permanent noise in `gds workspace audit --root
  ${HOME}/Developer`. It is already the state of every unanchored boundary on
  the device and is not introduced by this decision.
- No typed estate object changes, so no policy compilation, bundle, or
  projection input is affected by placing a repository in `external`.

## Rollback

Delete or relocate the external checkout. Nothing in the estate references it,
so no plan, approval, or regeneration is required to reverse this placement.
