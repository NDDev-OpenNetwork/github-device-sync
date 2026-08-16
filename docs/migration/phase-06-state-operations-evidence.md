# Phase 06 state and operation evidence

Status: local implementation complete; production mutation surface disabled.

Date: 2026-07-11

## Completed

- Added a CGo-free SQLite state store with embedded migration v1, WAL,
  `synchronous=FULL`, foreign keys, query-only inspection, and private path
  checks.
- Added immutable idempotent plans, ordered operation steps, an append-only
  event journal, result evidence, and durable terminal states.
- Added exact-scope repository locks, leases, heartbeats, monotonic fencing
  tokens, and refusal to steal expired locks.
- Added canonical plan digests and semantic plan validation in both the Go and
  temporary Python schema oracles.
- Added cryptographically random typed ULID generation without a new runtime
  package.
- Added the local orchestration engine with approval, expiry, handler,
  deterministic lock-order, precondition, apply, immediate verification,
  journal, and final verification gates.
- Added idempotent replay: a second apply for one plan returns the existing
  operation and does not call a handler again.
- Added `gds validate plan` and read-only `gds state inspect`.

## Evidence

```text
go test ./...
Go test: 95 passed in 21 packages

go test -race ./core/operations ./core/state
Go test: 16 passed in 2 packages

go vet ./...
PASS

python3 scripts/validate_gds_schemas.py
GDS schema validation: PASS

tools/test-sync.sh
64 checks, 0 failed

git diff --check
PASS

go mod tidy -diff
PASS (zero diff)
```

Tests prove:

- missing approval creates no operation and invokes no handler;
- expired and stale plans invoke no handler;
- an expired lock is not stolen;
- successful apply, immediate verify, explicit verify, and replay are durable;
- replay invokes apply exactly once in total;
- raw approval references are absent from journal evidence;
- handler failure is partial and preserves before/after evidence;
- concurrent lock acquisition has one winner;
- read-only state inspection does not create a missing database;
- all existing read-only CLI and legacy smoke tests remain green.

## Security review

- The local state directory and database reject group/other access.
- Database symlinks and permissive existing files are rejected.
- Approval evidence is reduced to a digest.
- SQL migrations and table names are internal constants; runtime values use
  parameters.
- Action handlers are explicit registrations, not shell command strings.
- No production mutation handler is registered.
- No GitHub token, secret, credential, remote instruction, or provider payload
  is stored.
- Expired leases do not authorize lock stealing.

## Files added or changed

- `core/state/`;
- `core/operations/`;
- `core/canonicaljson/` and `core/identity/` additions;
- `core/app/services.go` and `core/cli/root.go`;
- `schemas/v1/plan.schema.json` semantic validation and fixtures;
- `go.mod` and `go.sum` for `modernc.org/sqlite` v1.53.0;
- state/operation contracts, source register, migration plan, and tests.

## Not proven

- crash recovery during an actual provider or Git mutation;
- filesystem durability under sudden power loss beyond SQLite's declared
  `FULL` synchronous mode;
- multi-process controller failover;
- recovery/compensation commands;
- production action handlers;
- GitHub provider behavior;
- a release build with the required Go 1.26.5 builder;
- repository-wide Gitleaks in the current shell because the binary was not
  available on `PATH` during this phase.

## Next dependency

Implement bounded local Git state refresh/synchronization plus module and fork
plan builders over the engine. Mutation handlers remain unexposed until their
preconditions, path boundaries, fixtures, and recovery evidence pass.

## External approval required

None for the completed local foundation. Explicit approval remains required
before enabling any real mutation handler, installing runtime integrations,
changing GitHub, publishing, pushing, merging, releasing, or deleting.
