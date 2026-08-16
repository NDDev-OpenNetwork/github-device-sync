# C4 owner Git workflows evidence

Status: accepted local-provider gate

Date: 2026-07-11

Scope: session classification, non-integrating refresh, explicit checkout
synchronization, unfinished-work handoff, and dependency-ordered work
completion. All mutating fixtures used real temporary Git repositories and
local bare remotes. No live GitHub write was enabled or attempted.

## Completed

- Added `gds session start` with cached/read-only and explicit origin-refresh
  modes. Refresh persists the complete sorted origin-ref set, local HEAD,
  observation time, digest, and forced-update classification in state schema
  v5 without integrating the checkout.
- Added `gds sync checkouts --plan|--apply|--verify` for explicitly selected,
  clean, attached, origin-tracking, strictly-behind boundaries. Apply uses one
  exact fixed-argv fast-forward and never fetches, rebases, publishes, stages,
  or cleans.
- Added `gds handoff --plan|--apply|--verify` with an explicit changed-file
  set, exact content/status digests, commit identity, branch/ref evidence,
  checkpoint checks, and draft-PR policy. The isolated provider commits only
  approved paths and lease-pushes only to a local bare remote.
- Added `gds complete --plan|--apply|--verify`. It requires a canonical task
  identity, clean published task branches, current durable remote evidence,
  strict fast-forward ancestry, and an explicit checkout graph.
- Added dependency ordering for selected `git-submodule-consumer` relations.
  The consumer must pin the selected module's exact final task OID. Package
  consumers remain blocked until a release contract can produce a final
  package version.
- Added just-in-time precondition rechecks before the first mutation step of
  every repository. Drift after an earlier repository succeeds produces a
  durable partial saga and never calls untouched handlers.
- Added exact default-ref integration and task-ref cleanup through the isolated
  provider. Default, tracking, and task refs use compare-and-swap leases;
  cleanup occurs only after final reachability is proven.
- Hardened Git mutation roots by resolving all symlinked path components before
  evidence or mutation.
- Updated the owner Git workflow contract, verified Serena operation memory,
  and journaled control-plane projections.

## Live state evidence

The default controller state was migrated from schema 4 to schema 5 through
the explicit lifecycle transaction:

```text
state path:
  ~/.local/state/github-device-sync/state.db

plan digest:
  sha256:3b42bd31bdf68bd3a095d990832634541b3f30a6541bcf0b64a6fff484352431

before logical digest:
  sha256:5c2c68034b60ad71002d7acc2c9fb32ffd14f9dd3b659e9950b7d306b9d09653

after logical digest:
  sha256:ff356253990c181cb8de786425da308eafaf1e77bb39341790e7bb908e08f0d5

verified backup:
  ~/.local/state/github-device-sync/
    state.db.backup-v4-5c2c68034b60ad71.db
raw backup digest:
  sha256:2ce4ce50ce45a06bf01d367e43643122d43430d07e46640fefc54c9fd60b93fe
```

The lifecycle apply and explicit verification both succeeded.

## Completion dependency proof

A real two-repository fixture created:

```text
module task branch -> published module task OID
consumer task branch -> gitlink pinned to that exact module task OID
```

The stored completion plan ordered the module step before the consumer step.
Apply then proved:

1. module remote/local default advanced to the module task OID;
2. module task refs were removed only after reachability;
3. consumer remote/local default advanced afterward;
4. consumer main retained the exact finalized module gitlink;
5. both checkouts ended clean on `main` with task branches removed.

The provider also proved that a default or task branch active in another
worktree blocks before any remote ref changes.

## Final repository projection operation

```text
plan id:      plan_01KX8FY3J6KK835PN0SA7T5C7C
plan digest:  sha256:689d8f0284359b9742480b50e023e4ca7ca5958d4b4adeef64f64d17cb3c0c09
operation id: op_01KX8FY8XQK11MEA78EZF01FNE
input digest: sha256:42c461d0bcf4a535b4fe8eadbb0177858267ad0fec29f7f5cad5572c16b918dc
output digest: sha256:aa91c253a73671efcd911e6dfcb22cb872b23878e2f5e9a38d5778d054889b76
```

Apply and explicit verify succeeded. A subsequent projection check returned
zero findings. Approval evidence stored only a digest, and all four kill
switches were false in the operation report.

## Acceptance gates

| Gate | Evidence | Result |
|---|---|---|
| Session start never integrates or publishes | cached, refresh, forced-update, and missing-state fixtures | pass |
| Sync mutates only exact clean selected boundaries | approval, dirty, stale-plan, remote-advance, and verification fixtures | pass |
| Handoff never stages implicit files | explicit modified/untracked/deleted sets plus unrelated staged-state fixture | pass |
| Live handoff/complete publication is disabled | HTTPS remote is rejected before the operation engine or handler | pass |
| Completion finalizes dependencies first | real module/consumer gitlink saga | pass |
| Consumer main has no temporary dependency pin | final gitlink equals the finalized module default OID | pass |
| Cleanup preserves unproved or active work | strict publication/ancestry gates and multi-worktree fixture | pass |
| Cross-boundary drift cannot reach later handlers | operation-engine two-repository stale-state fixture | pass |
| Projection and memory provenance are current | journaled projection verify and seven verified memories | pass |

## Verification executed

```text
scripts/validate_go_core.sh --quick
go test ./... -count=1
go test -race ./core/operations ./core/providers/git ./core/gitops ./core/app ./core/cli -count=1
go vet ./...
python3 scripts/validate_gds_schemas.py
tools/test-sync.sh
gds generate repository --check --json
gds memory validate --json
```

Observed results:

- all Go packages passed, including race-enabled owner-workflow packages;
- schema validation passed;
- the legacy estate parity suite passed 64 of 64 checks;
- projection check returned zero findings;
- all seven Serena memories were verified with matching committed provenance.

## Not proven or intentionally deferred

- The installed Go toolchain is `go1.26.4`, below the registered security floor
  `go1.26.5`. Quick development validation passes, but release evidence remains
  `NOT_PROVEN` until the pinned builder is upgraded through its bootstrap
  transaction.
- Required check/review evidence, pull-request integration, draft-PR creation,
  and live GitHub push remain disabled until the separately permissioned live
  provider is accepted.
- Package finalization is blocked until the C5 module release contract exists.
- No GitHub App, repository setting, branch, PR, release, deployment, ruleset,
  or permission was changed.

## Next dependency

C5 may now implement repository, module, fork, workspace, and portfolio
lifecycle commands on the accepted C3 transaction engine and C4 Git workflows.
Every live provider write remains gated separately.
