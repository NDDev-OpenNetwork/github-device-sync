# ADR 0032: Place every fork checkout in one flat forks workspace root

Status: Accepted

Supersedes: the owner-specific fork placement established in ADR 0018 and
reaffirmed in ADR 0026

Date: 2026-07-27

## Context

ADR 0018 made fork workspaces owner-specific so equal repository names could not
collide across owners: `portfolio:organization-forks` resolved to
`${HOME}/Developer/forks/nddev` and `portfolio:personal-forks` to
`${HOME}/Developer/forks/example-user`. ADR 0026 flattened the server roots for the
same reason this decision now flattens the fork roots, but explicitly left forks
alone — "owner-specific roots remain correct for forks, where cross-owner name
reuse is expected."

That reasoning rested on a collision risk the estate does not actually carry.
The organization owns **zero** forks; every fork in the estate — nine of them —
belongs to `example-user`, and no name appears under both owners. The split has
therefore never separated anything: `portfolio:organization-forks` selects an
empty set, and the owner level exists to disambiguate names that do not collide.

Neither fork root has ever been materialized. `${HOME}/Developer/forks` does not
exist on `example-user-mac1` at all, so the declared layout describes a directory
tree that was never built — the same standing disagreement between declaration
and reality that ADR 0026 resolved for servers.

A device may not assign one workspace root to two selectors: both
`core/validation/schema.go` and `scripts/validate_gds_schemas.py` raise
`GDS_DEVICE_WORKSPACE_ROOT_REUSED`. Pointing the two existing fork selectors at
one flat root is therefore rejected by validation, and one flat root requires one
fork portfolio.

A fork is addressed by what it forks, not by which account holds it. Splitting
forks by owner adds a directory level that carries no operational meaning.

## Decision

1. `portfolio:organization-forks` and `portfolio:personal-forks` are replaced by
   a single `portfolio:forks`.
2. Both fork selectors keep their own `match` blocks and IDs. Selection stays
   owner-scoped, so a repository is still classified by the account that owns it;
   only the assigned portfolio is now shared.
3. Every device declares one `forks` workspace root at `${HOME}/Developer/forks`
   and exactly one materialization assignment for `portfolio:forks`. The
   per-owner `forks/nddev` and `forks/example-user` subroots are removed.
4. Fork checkout placement is `${HOME}/Developer/forks/<provider-repository-name>`,
   flat, regardless of owning account.

## Consequences

- The estate has one placement rule per kind of repository. Forks, servers, and
  projects no longer each answer the owner question differently.
- Two forks with the same name under different owners would resolve to one path.
  Layout analysis already rejects duplicate local paths, so such a collision
  surfaces as `GDS_WORKSPACE_PATH_CONFLICT` rather than silently overwriting a
  checkout. No such pair exists today.
- `fork-default` matches one portfolio instead of two, and both owner records
  name the same `fork_portfolio`.
- The estate compiler's fork assignments now carry `portfolio:forks`;
  `core/estate/compiler_test.go` asserts the shared portfolio while still
  asserting the distinct matched selectors, mirroring what ADR 0026 did for
  servers.

## Rollback

Restore the two portfolio names in the fork selectors, the two owner records, the
`fork-default` match list, and the two per-owner roots and assignments in each
device descriptor. No provider state is involved, so rollback is a source-only
change followed by regeneration.
