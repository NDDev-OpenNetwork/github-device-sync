# Device workspace cutover evidence

Status: local device workspace cutover complete; external rollout not included

Date: 2026-07-11

Plan: `docs/migration/device-workspace-cutover-plan.md`

## Legacy metadata preservation

Each metadata repository was branched from its unchanged remote `main`, its
complete local context diff was scanned through Gitleaks, committed, pushed,
and verified against the remote branch OID. The worktree was then returned to
clean `main`.

| Repository | Preserved branch | Verified remote OID |
|---|---|---|
| `example-org/nddev-monorepo` | `archive/gds-pre-c0-context-20260711` | `8dde22b8f88df7870700ea19d082785b09bbbcb7` |
| `example-org/forks-monorepo` | `archive/gds-pre-c0-context-20260711` | `e0f8db39594f61b22104f1d4fcd90422e934140d` |
| `example-user/example-user-monorepo` | `archive/gds-pre-c0-context-20260711` | `b7dc7737725090f5a654621ff6b0b40a516e7698` |

The branches are rollback evidence. They are not accepted projections and are
not merged into metadata repository `main`.

## Relocated checkouts

| Provider repository | Provider ID | Fork | Destination | Verified OID |
|---|---:|---:|---|---|
| `example-org/nddev-ci-workflows` | `1289065451` | no | `${HOME}/Developer/nddev/nddev-ci-workflows` | `ac4d1f469f5974741c7449305ffcbd5f05a5a47f` |
| `example-org/example-harnesses` | `1295636250` | no | `${HOME}/Developer/nddev/example-harnesses` | `0407a1a48d9fd9845b374c5930e8ebb4ab94c66c` |
| `example-org/nddev-stroyme` | `1293770903` | no | `${HOME}/Developer/nddev/nddev-stroyme` | `29fafe83cd400cc4b2481cae50fca7278a9c9221` |
| `example-org/rldyour-ai-cli-tools` | `1244982818` | no | `${HOME}/Developer/nddev/rldyour-ai-cli-tools` | `f2bed13a5e4e856e29bdb8af454f3f8871241b6a` |
| `example-user/example-user` | `974687860` | no | `${HOME}/Developer/example-user/example-user` | `669a40bdf6c73cb0917e4f145e83626e7f9b37c1` |

For every destination the following checks passed after the move:

- Git top-level equals the declared destination;
- local HEAD equals the expected and remote `main` OID;
- staged, tracked, untracked, and conflict state is empty;
- exactly one worktree exists;
- every recursive submodule gitlink is initialized and exact;
- GitHub repository ID, fork flag, default branch, and archive state match the
  planned provider classification.

## Topology retirement

Commit `be0aa1e` removed the three metadata gitlinks and root `.gitmodules`
after the archive branches above were verified. Typed portfolio and device
workspace assignments are now the active topology. Commit `e6ba28e`
quarantined the legacy engine pending final parity retirement.

## Remaining cutover work

- remove the quarantined legacy engine only at C12 after parity and rollback
  acceptance.

## Current layout audit

Before control-plane relocation, the source-bound `gds workspace audit`
classified 14 anchored Git boundaries: seven standalone checkouts and seven
embedded submodules. Thirteen were compliant and the only drift was the
control-plane source path.

The seven module checkouts remain under their two superprojects. Their
temporary gitlink movement belongs to the unpublished onboarding branches and
is not device-placement drift.

## Control-plane relocation

The clean checkout at commit
`8e3cd823105527a15e6fc71481e1c16508741002` moved from
`~/Desktop/github/github-device-sync` to
`~/Developer/control-plane/github-device-sync`.

Preconditions and postconditions:

- the source worktree and index were clean;
- exactly one worktree existed;
- the target did not exist;
- Git top-level, repository identity, remote, branch, and HEAD were unchanged;
- active Codex trust, Claude context, Pi context, Serena registry, and Git
  maintenance pointers were updated before the move;
- backup, session, process, artifact, and log records were retained as
  historical evidence rather than rewritten;
- the root generated projection was rematerialized through operation
  `op_01KX9QWGXF7B8YNQ5ACQC30GTS` and verified before commit `9f7b68f`.

The post-cutover workspace audit reports 14 discovered and anchored
repositories, seven standalone and seven embedded, with 14 compliant, zero
drifted, zero invalid, and no findings.
