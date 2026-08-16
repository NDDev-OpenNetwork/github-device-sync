# C3 production mutation and recovery evidence

Status: accepted local gate

Date: 2026-07-11

Scope: local control-plane state, bounded filesystem materialization, local Git
recovery references, operation journaling, and conservative recovery. No
GitHub/provider write was enabled or attempted.

## Completed

- Added strict operation-step idempotency keys and compare-and-swap
  reconciliation cursors in state schema v4.
- Made ordinary state opens non-creating and non-migrating. Added explicit
  digest-bound `state initialize` and `state migrate` plan/apply/verify flows.
- Added consistent private SQLite backups, logical database fingerprints,
  exclusive migration precondition checks, and durable lifecycle evidence.
- Added query-only operation inspection with plan, step, event, lock, and
  payload-digest integrity checks.
- Added conservative operation recovery as a separate saga. Only an expired
  current-device lock with a proved-dead PID and exact immutable scope may be
  changed automatically.
- Added explicit compensation reporting without automatic rollback or handler
  retry.
- Added strict fail-closed kill switches and scope-bound approval evidence.
- Replaced raw durable handler errors with stable codes and added bounded
  credential redaction for Git stderr.
- Hardened projection writes with `os.Root`, stable file identity checks,
  root-relative atomic rename, file/directory fsync, bounded backups, and
  symlink-race tests.
- Restricted read-only Git to exact command/argument shapes and process-group
  cancellation.
- Added one isolated local Git mutation primitive: compare-and-swap of
  `refs/gds/recovery/*`. It cannot change branches, tags, HEAD, index,
  worktrees, remotes, or provider state and has no standalone CLI escape hatch.

## Live local state migration

The default state database was migrated through the new lifecycle contract:

```text
state path:
  ~/.local/state/github-device-sync/state.db

plan digest:
  sha256:f96e9e3b278585b22356631ecbcea968e794216eab700ea4b8694541e7860f77

before:
  schema: 3
  logical digest:
    sha256:d0861cc1789ceed158c284c7b387772a066f3347facad326f42d32f096b39ceb

after:
  schema: 4
  logical digest:
    sha256:faad64d189adde6b1b35ab7702c80fdebb162b03f6e16923e20fc4ce6ecba5f7

backup:
  ~/.local/state/github-device-sync/
    state.db.backup-v3-d0861cc1789ceed1.db
  logical digest:
    sha256:d0861cc1789ceed158c284c7b387772a066f3347facad326f42d32f096b39ceb
  raw digest:
    sha256:09f02b6d71729c841b05bf537626a307629cd3d1a8ee8748bcd829055c5004f5
```

Apply and verify both succeeded. The approval reference was stored only as
digest
`sha256:66bae5ee9708e848b84c06d508cdfd8cc78c1ed7df1593611bd8ddd9bad0850e`.

Post-migration query-only state inspection reported schema 4, WAL, foreign
keys enabled, query-only mode, 10 plans, 9 operations, 9 steps, 59 events, and
zero locks at the inspection point.

## Final repository projection operation

```text
plan id:      plan_01KX8BKMVKSK9Z2TXM318D2XB5
plan digest:  sha256:3ad2718ba8c6f144e8be41f4ca2d45cd85efc4495436a4bfc017299b1e68a73c
operation id: op_01KX8BKWN7QS0H362XGA4W5BPA
input digest: sha256:db76000277764cd6da90c3ce28d362be8101289dce4aba7309ef34a47150dbf4
output digest: sha256:2eceb4ac4df31a72d7275e442de6037b86f817b27bbcf97410a34232b4f11aaa
```

Apply and explicit verify succeeded. A subsequent projection check returned
zero findings. All four kill switches were false and were present in the
operation reports.

## Acceptance gates

| Gate | Evidence | Result |
|---|---|---|
| No mutation without plan and approval | state lifecycle, recovery, recovery-ref, and operation-engine tests | pass |
| Expired, stale, changed, or tampered preconditions stop before handlers | plan, operation, migration, and stale-ref tests | pass |
| Concurrent apply has one mutation winner | race-enabled operation concurrency test | pass |
| Interrupted state is recoverable or explicitly manual-only | pending, applying, succeeded, failed, terminal, live-PID, remote-device, and missing-lock cases | pass |
| No blind retry or automatic compensation | recovery decision and handler contracts | pass |
| Traversal and symlink races cannot escape root | `os.Root` target/parent race and traversal tests | pass |
| Shell/argument injection, output caps, cancellation, and stderr redaction | bounded Git runner and redaction tests | pass |
| Journal/events remain append-only and raw handler secrets are absent | state triggers and operation redaction tests | pass |
| Live v3 state has verified v4 backup and migration evidence | exact lifecycle apply/verify above | pass |

## Verification executed

```text
go test ./...
go test -race ./core/state ./core/operations ./core/app
go test -race ./core/providers/git ./core/gitops
go test -race ./core/materialize ./core/projections
go vet ./core/state ./core/operations ./core/app ./core/cli
go vet ./core/providers/git ./core/gitops
python3 scripts/validate_gds_schemas.py
bash -n tools/sync.sh tools/test-sync.sh
tools/test-sync.sh
gds memory validate --json
gds generate repository --check --json
gds state inspect --json
gds operation inspect op_01KX8B4BRFW7YV8FQ44E90M042 --json
```

Observed results:

- all Go packages passed after the verified memory refresh;
- race-enabled focused suites passed;
- schema validation passed;
- legacy estate smoke suite passed 64 of 64 checks;
- seven Serena memories were verified;
- the inspected projection operation had journal integrity `pass`, one
  succeeded content-derived idempotency key, and zero locks.

## Not proven or intentionally deferred

- Session, sync, handoff, completion, module, fork, repository, and workspace
  owner workflows are C4-C5 work and are not claimed complete here.
- No GitHub App, provider write, push, PR, merge, release, deployment, ruleset,
  or repository-setting mutation was performed.
- Remote-device stale-lock recovery and any applying-step side effect remain
  manual-only by design.
- Multi-controller distributed leases remain a later controller deployment
  concern; C3 proves the single-controller local contract.

## Next dependency

C4 may now build session start, bounded checkout synchronization, handoff, and
complete-work workflows on the accepted operation/recovery platform. Those
commands must reuse these handlers and gates rather than bypass them.
