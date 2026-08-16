# Device workspace cutover plan

Status: executed locally; all declared checkout placements verified

Date: 2026-07-11

Decision: `docs/adr/0018-device-workspaces-and-metadata-repository-retirement.md`

## Preconditions

- All source checkout HEADs equal the observed remote default-branch OIDs.
- Source checkout worktrees and indexes are clean.
- Direct metadata repository changes are preserved on verified remote archive
  branches before any local removal.
- Destination paths do not exist.
- No repository has an auxiliary worktree.
- A changed precondition makes this plan stale.

## Exact path map

| Repository | Provider classification | Source | Destination | Expected OID |
|---|---|---|---|---|
| `nddev-ci-workflows` | organization source | `nddev-monorepo/nddev-ci-workflows` | `${HOME}/Developer/nddev/nddev-ci-workflows` | `ac4d1f469f5974741c7449305ffcbd5f05a5a47f` |
| `example-harnesses` | organization source | `nddev-monorepo/example-harnesses` | `${HOME}/Developer/nddev/example-harnesses` | `0407a1a48d9fd9845b374c5930e8ebb4ab94c66c` |
| `nddev-stroyme` | organization source | `nddev-monorepo/nddev-stroyme` | `${HOME}/Developer/nddev/nddev-stroyme` | `29fafe83cd400cc4b2481cae50fca7278a9c9221` |
| `rldyour-ai-cli-tools` | organization source | `nddev-monorepo/rldyour-ai-cli-tools` | `${HOME}/Developer/nddev/rldyour-ai-cli-tools` | `f2bed13a5e4e856e29bdb8af454f3f8871241b6a` |
| `example-user` | personal source | `example-user-monorepo/example-user` | `${HOME}/Developer/example-user/example-user` | `669a40bdf6c73cb0917e4f145e83626e7f9b37c1` |

Source paths in the table are relative to the former control-plane root. The
control-plane checkout moved only after every active pointer was ready for an
atomic update. Its exact destination is
`${HOME}/Developer/control-plane/github-device-sync`, not the workspace root
directory itself.

The control-plane move was executed from clean commit
`8e3cd823105527a15e6fc71481e1c16508741002`. External branch publication is a
separate GitHub approval boundary and was not implied by the local cutover.

## Apply order

1. Recheck every precondition and provider classification.
2. Create destination parent directories only.
3. Move one clean checkout at a time.
4. Verify HEAD, origin, status, worktrees, and recursive submodule gitlinks at
   the destination before moving the next checkout.
5. Remove legacy root gitlinks and `.gitmodules` on the root feature branch.
6. Validate that no active source, policy, schema, skill, or projection uses a
   metadata repository as topology authority.
7. Run `gds workspace audit` across the current and declared workspace roots.
   A valid pre-cutover report contains exactly one placement drift: the
   control-plane checkout itself. Embedded submodules must resolve through
   typed superproject relationships rather than standalone device targets.

## Failure handling

Stop after the first failed verification. Move only the failed checkout back
to its source path, reverify its expected OID, and leave already verified
moves in place with a partial-completion record. Never clone over, merge into,
or delete an existing destination.

## External boundary

This cutover does not delete or archive GitHub repositories, change settings,
merge branches, release artifacts, or install harnesses. Those actions require
their own exact plans and evidence.

Execution evidence: `docs/migration/device-workspace-cutover-evidence.md`.
