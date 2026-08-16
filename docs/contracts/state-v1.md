# GDS local state v1 contract

Status: implemented local foundation; no provider mutation enabled.

## Authority boundary

The SQLite database is local observed and operational state. It is not desired
estate configuration and is never a substitute for repository manifests,
policies, Git objects, or current provider evidence.

The default path is:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/github-device-sync/state.db
```

`XDG_STATE_HOME` must be absolute. Creating a writable database requires a real
private parent directory and a regular non-symlink database with mode `0600` or
stricter. The opened parent and database identities must remain the objects
named by their absolute paths after SQLite opens the database; owner-private
WAL and SHM sidecars are verified under the same opened directory authority.
Read-only inspection never creates the path.

Every Store holds a shared advisory lock on
`<state.db>.gds-authority.lock` from before SQLite `Ping` until `Close`. A
cooperative pathname migration or replacement must first acquire the exclusive
form of that lock. Every direct SQL execution/query boundary checks the opened
parent, canonical parent pathname, database identity, canonical database
pathname, lock identity, and current sidecar safety before and after the call.
Streaming query rows recheck on `Next`, `Scan`, `Err`, and `Close`. Transaction
creation checks before `BEGIN`; every Store transaction commit rechecks
immediately before and after `COMMIT`. Snapshot and migration paths perform
equivalent checks around their reserved connection boundaries.

This is a single-user, cooperative-process authority model. It rejects a
replacement that remains visible at any checked boundary and prevents an
actor honoring the advisory lock from racing open or an operation. It does not
claim protection from a process running as the same UID that intentionally
ignores the lock and performs a complete pathname ABA between two checks.
Portable descriptor-bound SQLite/WAL identity would require a separately
reviewed VFS; that stronger adversary model is explicitly NOT_PROVEN.

## SQLite profile

The selected driver is the CGo-free `modernc.org/sqlite` v1.53.0 package. The
store configures:

- WAL journal mode;
- `synchronous=FULL`;
- foreign keys enabled;
- a bounded busy timeout;
- one connection for the single-controller local deployment;
- `query_only=ON` for read-only inspection.

The current schema version is 8. It contains plans, operations, operation
steps, append-only events, repository locks, fencing counters, webhook queue,
repository observations, reconciliation runs, remote-refresh evidence, accepted
bundles, rollout plans, rollout targets, rollout events, exact-plan enablements,
signed device evidence, a bounded telemetry outbox, and metadata. Schema
migrations are embedded, ordered, gap-checked, and applied transactionally.

Opening state for an ordinary command is strict: it never creates a missing
database and never migrates an older schema. Self-hosting lifecycle changes use
their own transaction grammar:

```text
gds state initialize --plan
gds state initialize --apply <plan-digest> --approval-ref <signed-approval.json> --enable <plan-id>
gds state initialize --verify <plan-digest>

gds state migrate --plan
gds state migrate --apply <plan-digest> --approval-ref <signed-approval.json> --enable <plan-id>
gds state migrate --verify <plan-digest>
```

The lifecycle plan binds the absolute state path, expected presence, exact
source schema and logical database digest, target schema, and deterministic
backup path. Apply recomputes the plan after acquiring SQLite's exclusive write
boundary. A migration checkpoints WAL, creates and fsyncs a private consistent
backup, verifies its logical digest, applies ordered migrations atomically, and
records only the signed approval digest. A stale digest, invalid signature,
missing exact-plan enablement, or missing approval
blocks before the logical state changes.

## Immutable plans

The plan identity, operation, digest, body, creation time, and expiry cannot be
updated after insertion. Re-inserting byte-identical immutable content is
idempotent. Reusing a plan ID for different content returns a conflict.

Only the explicit plan state machine may change:

```text
planned -> approved -> applying -> succeeded | failed | partial
planned | approved -> stale
```

## Append-only journal

Every started operation owns ordered steps and an append-only event stream.
SQLite triggers reject event updates, deletes, and events whose plan does not
match the operation. Event payloads carry SHA-256 digests.

Operation and step terminal states preserve result evidence and errors. A
partial operation is not silently retried or described as success.

An approved operation starts as one transaction: the plan compare-and-swap is
the first statement and obtains SQLite writer serialization before lock
inspection; the operation and step journal, approval evidence, and complete
repository lock set then either all commit or all roll back. A lock conflict
leaves the plan reusable in `planned` state and creates no operation. Terminal
step transitions, operation and plan status, terminal events, and release of
the exact fenced lock set also commit as one transaction. A failed terminal
commit leaves the operation nonterminal and retains its locks for explicit
recovery.

Every new operation step stores an immutable SHA-256 idempotency key derived
from the exact plan digest and step. Migration v4 preserves older rows with an
explicit `legacy:` key instead of inventing unverifiable historical evidence.

## Locks and leases

Repository mutations use exact-scope locks with:

- operation, device, session, and process identity;
- acquisition, heartbeat, and lease timestamps;
- a monotonically increasing fencing token.

An expired lock is never stolen by an ordinary operation. Explicit recovery
inspects the owner, journal, process/session evidence, and repository state.
Only an expired current-device lock with a proved-dead PID is eligible for the
bounded recovery transaction. Heartbeat and stale-lock recovery compare the
exact lease generation. Ordinary release compares scope, lock identity,
operation, and fencing token. Atomic operation finalization additionally
requires the complete operation lock count plus device, session, PID, and
fencing evidence; lease timestamps may have advanced through a valid heartbeat.

## Inspection

```text
gds state inspect [--path <state.db>]
```

The command opens the database in query-only mode and returns schema/runtime
metadata plus record counts. A missing database returns
`GDS_STATE_NOT_INITIALIZED` and does not create a directory or file.

`gds operation inspect <operation-id>` additionally reads an immutable plan,
operation, ordered steps, append-only events, and matching locks. It recomputes
event digests and never repairs a failed journal automatically.

Reconciliation cursors provide compare-and-swap sequence numbers and content
digests, so a stale worker cannot overwrite a newer durable cursor. Current
controller reconciliation runners start a fresh run after restart; resumable
cursor consumption is an available state primitive, not a claimed runtime
behavior.

Scheduled controller backups use `VACUUM INTO`, fsync the file and parent
directory, then require the current schema version, `PRAGMA quick_check`,
`PRAGMA foreign_key_check`, and a non-empty deterministic logical digest. A
candidate that fails any verification is removed and is never reported as a
verified backup.

Lifecycle verification requires the exact applied plan digest and rechecks the
current schema, integrity, logical digest, and durable metadata. Initialization
and migration reports include all four effective kill switches.

## Bundle and rollout state

Accepted bundle evidence is append-only and keyed by trust domain plus release
sequence. A sequence cannot be rebound to another digest. The highest accepted
sequence remains the anti-rollback floor even after an explicitly authorized
downgrade is recorded.

Rollout plan bytes, target-set digest, bundle identity, and creation time are
immutable. Target and rollout transitions use compare-and-swap preconditions.
A wave cannot advance or complete until every target in the active wave is
successful. Rollout events are append-only.

## Non-goals in this phase

- no live GitHub credential or provider write is enabled;
- no multi-controller lease backend exists;
- no defense against a same-UID process that deliberately ignores the state
  authority lock and completes a pathname ABA between checks is claimed;
- remote-device and unknown-side-effect recovery remains manual-only;
- no secret is stored in the database;
- no production mutation handler is registered.
