# ADR 0018: Use device workspaces instead of metadata repositories

Status: Accepted

Superseded in part by: ADR 0027 (the reusable-module dual-placement clause below)
and ADR 0032 (the owner-specific fork workspace roots; fork placement is now one
flat root)

Date: 2026-07-11

## Context

The legacy root tracks `nddev-monorepo`, `example-user-monorepo`, and
`forks-monorepo` as Git submodules. Those repositories contain metadata and
nest independent project checkouts without tracking their source trees. The
shape conflates portfolio classification, local placement, Git topology, and
agent context.

The target model already separates stable repository identity, logical
portfolios, Git relationships, and device materialization. Keeping the three
metadata repositories would preserve a second topology authority and would
make a local path look like repository identity.

## Decision

1. Device checkout placement is declared by typed assignments in
   `estate/devices/*.yaml`: a portfolio selector resolves to one named
   `workspace_root` and one materialization mode.
2. Workspace roots are ordinary device directories, not Git repositories and
   not logical parents.
3. A checkout path is `<resolved-workspace-root>/<provider-repository-name>`.
   Provider identity and the repository-owned `.gds/repository.yaml` remain
   authoritative; the path is only a device locator.
4. Fork portfolios use owner-specific roots so equal repository names cannot
   collide across owners.
5. The three legacy metadata repositories are retired from active topology.
   Their unmerged work is preserved on explicit remote archive branches before
   local removal. Their remote repositories remain temporarily available for
   rollback and are not GDS authorities.
6. The control-plane repository moves to its declared control-plane workspace
   only after its generated context and system pointers can be updated and
   verified as one plan/apply/verify operation.
7. A standalone checkout is selected by device policy. An embedded submodule
   instead inherits its locator from the Git-reported superproject and one
   typed `git-submodule-consumer` relationship. These modes are audited
   independently.

## Consequences

- Portfolio-wide operations no longer imply one Git transaction.
- Project and fork checkouts can be materialized independently on each device.
- The control plane has no project gitlinks and no manually maintained
  container registry.
- Legacy `tools/sync.sh` topology commands become migration-only parity inputs
  and must be removed after target workspace workflows pass.
- Repository collisions are rejected during planning rather than resolved by
  implicit directory naming.
- A reusable module may exist as a standalone checkout on one device and as an
  embedded submodule in a consumer; each observed Git boundary is classified
  by its actual Git mode rather than by repository role alone.
  **Superseded by ADR 0027:** a module that is actually consumed through a
  `git-submodule-consumer` relationship gets no standalone checkout on any
  device. Classification by observed Git mode stands; the dual-placement
  allowance does not. A module with no incoming submodule consumer remains
  standalone-eligible, so this clause still governs that case.

## Verification

- Every device assignment references an existing named workspace root.
- A portfolio selector occurs at most once per device profile.
- Every moved checkout retains the same HEAD, origin identity, provider
  repository ID, worktree status, and submodule gitlinks.
- The old and new paths are recorded in migration evidence.
- Root `.gitmodules`, gitlinks, docs, tests, and generated projections contain
  no active dependency on the retired repositories after cutover.

## Rollback

Before cutover, preserve each dirty metadata repository on a verified remote
archive branch and record every checkout OID. A local checkout can be moved
back using the inverse path map. The metadata repositories can be cloned at
their recorded OIDs, but they do not regain authority unless this ADR is
superseded by a new accepted decision.
