# ADR 0026: Place every server checkout in one flat servers workspace root

Status: Accepted

Supersedes: the owner-specific `servers` roots described in ADR 0025

Superseded in part by: ADR 0032 (the clause reserving owner-specific roots for
forks; forks now use one flat root too)

Date: 2026-07-26

## Context

ADR 0018 made fork workspaces owner-specific so equal repository names cannot
collide across owners, and the server selectors were modelled the same way:
`portfolio:organization-servers` resolved to `${HOME}/Developer/servers/nddev`
and `portfolio:personal-servers` to `${HOME}/Developer/servers/example-user`.

That owner split was never the intended shape for servers, and it was never
materialized. On `example-user-mac1` neither subroot exists; every server
checkout lives directly in `${HOME}/Developer/servers`. The declared layout and
the actual one have disagreed since the roots were declared, which is why
`gds workspace audit` reports `GDS_WORKSPACE_PLACEMENT_DRIFT` for
`servers/ci-workflows`.

A device may not assign one workspace root to two selectors: both
`core/validation/schema.go` and `scripts/validate_gds_schemas.py` raise
`GDS_DEVICE_WORKSPACE_ROOT_REUSED`, proven by a negative fixture in
`tests/fixtures/schemas/v1/cases.json`. Pointing the two existing server
selectors at one flat root is therefore rejected by validation, and one flat
root requires one server portfolio.

Servers are addressed by host, not by owning account. Splitting them by owner
adds a directory level that carries no operational meaning.

## Decision

1. `portfolio:organization-servers` and `portfolio:personal-servers` are
   replaced by a single `portfolio:servers`.
2. Both server selectors keep their own `match` blocks and IDs. Selection stays
   owner-scoped and `server-`-prefixed, so a fork named `server-*` still lands
   in its owner's fork portfolio; only the assigned portfolio is now shared.
3. Every device declares one `servers` workspace root at
   `${HOME}/Developer/servers` and exactly one materialization assignment for
   `portfolio:servers`. The per-owner `servers/nddev` and `servers/example-user`
   subroots are removed.
4. Server checkout placement is `${HOME}/Developer/servers/<provider-repository-name>`,
   flat, regardless of owning account.

## Consequences

- The declared layout now matches the materialized one, removing a standing
  source of placement drift.
- Two server repositories with the same name under different owners would
  resolve to one path. Layout analysis already rejects duplicate local paths, so
  such a collision surfaces as a finding rather than silently overwriting a
  checkout. Server repositories are host-named and unique in practice.
- Owner-specific roots remain correct for forks, where cross-owner name reuse is
  expected. This decision does not change fork placement.
- The estate compiler's server assignments now carry `portfolio:servers`;
  `core/estate/compiler_test.go` asserts the shared portfolio while still
  asserting the distinct matched selectors.

## Rollback

Restore the two portfolio names in the server selectors and the two per-owner
roots and assignments in each device descriptor. No provider state is involved,
so rollback is a source-only change followed by regeneration.
