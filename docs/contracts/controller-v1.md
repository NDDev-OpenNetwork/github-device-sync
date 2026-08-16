# GDS controller and webhook v1 contract

Status: production-capable local single-controller service implemented; no
public service, GitHub App, or endpoint deployed.

## Runtime boundary

`gds-controller --runtime-config <private-file>` loads an exact private
controller runtime, canonical estate intent, and private GitHub runtime. It
constructs read-only installation clients only. The service binds to an
explicit loopback IP; TLS termination and public ingress belong to a separately
approved reverse proxy deployment.

Endpoints:

```text
GET  /healthz
GET  /readyz
GET  /metrics
POST /github/webhook
```

HTTP header/body/time limits are bounded. Logs are JSON and exclude webhook
payloads, credentials, arbitrary provider errors, and private diffs.

`shutdown_timeout_seconds` is one total shutdown budget, not an HTTP-only limit.
The cancellation path shares a single deadline between HTTP draining and the
background worker drain, so a worker, reconciliation, or backup run that ignores
cancellation cannot hold the process — or its SQLite lifetime authority — open.
An exhausted budget returns a typed shutdown-timeout error instead of blocking,
and the standalone process exits non-zero so a successor cannot assume the
previous owner released its authority cleanly.

Every full reconciliation must also create a private schema-validated Ed25519
audit snapshot. The signing key comes from its own logical secret reference;
the expected public key is pinned in controller runtime configuration. The
signature covers snapshot, estate, reconciliation, timestamp, payload digest,
and result fields. A self-described replacement key is rejected, and a run
cannot report `succeeded` when snapshot creation fails.

## Webhook ingress

The HTTP receiver:

1. accepts POST JSON only;
2. validates delivery and event identity;
3. enforces a versioned event/action allowlist;
4. reads a bounded payload;
5. verifies `X-Hub-Signature-256` with HMAC-SHA256 and constant-time comparison;
6. validates JSON;
7. durably enqueues before returning `202`;
8. performs no Git or GitHub work in the request handler.

Duplicate delivery ID plus identical event/body is idempotent. The same ID with
different content is a security conflict. Unknown events/actions, invalid
signatures, and oversized bodies are not queued.

## Durable worker

Workers atomically claim one available delivery, execute an injected processor,
and mark success, bounded retry, or dead-letter. Retry backoff is bounded.
Arbitrary processor errors are not persisted; only generic text or an explicit
safe diagnostic is stored.

Processing claims have a required visibility timeout between 60 seconds and 24
hours; the reference runtime uses 3600 seconds. A claim is stale when
`claimed_at <= now - webhook_processing_timeout_seconds`. Reclaiming a stale
delivery increments its attempt and replaces `claimed_at`; completion compares
that exact claim timestamp, so a previous worker cannot overwrite a newer
attempt. A stale claim already at the attempt limit becomes dead-letter.
An already-failed delivery at or above a newly lowered attempt limit also
becomes dead-letter during the next claim transaction instead of remaining
permanently unclaimable. Processing is therefore at-least-once across
controller crashes. Version 1 has no claim heartbeat, so the configured timeout
must exceed the longest supported processor execution time.

This worker contract is read-only with respect to GitHub. Mutating processors
are not registered.

Repository-bearing events perform a targeted authoritative provider read and
persist `available`, `inaccessible`, `auth-failed`, or `unknown` independently.
A provider 404 is never interpreted as deletion. Installation-wide events run
the same bounded full reconciler used by scheduled observation.

Governance-related events persist a typed snapshot: repository identity, merge
and available security settings, repository Actions policy, workflow-token
defaults, and at most 100 effective repository rulesets. High-volume
code/check events perform one authoritative metadata read. Full scheduled
inventory remains one bounded installation enumeration per account; it does
not perform four extra requests for every repository.

## Effective provider permissions

Each installation descriptor declares an exact repository selection and exact
permission maps. The current Inventory App contract allows only repository
`actions`, `administration`, `checks`, `contents`, `metadata`, and
`pull_requests` at `read`, with no organization permission. The installation
token response must match byte-for-byte at the semantic map level before any
provider data request. Missing, extra, stronger, malformed, or differently
scoped permissions block the read as a security finding.

Evidence contains only the expected/effective maps and repository selection;
the token is never serialized. `gds github governance` reads one exact
repository and returns `comparison: observed-only` because desired mutation
governance belongs to C8 and is not yet declared.

## Reconciliation

Full reconciliation enumerates each configured installation with bounded
parallelism, isolates installation failures, combines at most 2000 repository
observations, compiles selector-based desired assignments, and reports drift.
Webhook events are hints; authoritative provider reads remain required before a
decision.

Local state migrations v2 and v3 store:

- deduplicated webhook deliveries and queue state;
- freshness-ordered repository observations;
- reconciliation run journals;
- existing plans, operations, events, and fenced locks.
- append-only accepted bundle evidence and anti-rollback history;
- immutable rollout plans, target state, and append-only rollout events.

Unavailable repositories use explicit access states; inaccessible is never
interpreted as deleted.

## Backup and retention

The service creates a consistent SQLite `VACUUM INTO` snapshot in a private
directory, changes it to `0600`, fsyncs it, and requires the current schema
version, `PRAGMA quick_check`, `PRAGMA foreign_key_check`, and a deterministic
logical digest before syncing the parent directory. Verification failure
removes the candidate. The service records both raw-file and logical digests
and never overwrites an existing snapshot.

After a successful backup it retains only strict generated backup filenames,
then prunes terminal webhook records and reconciliation journals using the
schema-bounded retention policy. Mutation operation/security journals are not
deleted by controller maintenance.

## Observability

Metrics include ingress accept/reject counts, worker outcomes, dead letters,
durable queue gauges, reconciliation outcomes, observation/journal counts, and
backup outcomes. Reconciliation journals preserve provider request IDs, rate
headers, current assignments, and stable error codes without tokens or raw
provider error bodies.
