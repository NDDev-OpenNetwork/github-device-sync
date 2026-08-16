# ADR 0009: Plan, apply, verify, and journal every mutation

Status: Accepted

Date: 2026-07-11

## Context

Git and GitHub operations across repositories are not one atomic transaction.
Legacy commands combine observation, decision, and mutation.

## Decision

Every mutation follows:

resolve, observe, plan, validate, approve, recheck, apply, verify, journal.

Plans contain:

- exact scope;
- expected local and remote object IDs;
- manifest and policy digests;
- expiry;
- approval class;
- ordered steps;
- compensation metadata;
- plan digest.

Cross-repository operations are resumable sagas with per-step idempotency and
durable journals. Changed preconditions return STALE_PLAN.

## Consequences

- Partial completion is explicit and recoverable.
- Agent text is not mutation authorization.
- CLI and provider adapters need stable result schemas and exit classes.

## Alternatives considered

- Best-effort shell sequence: rejected because retry and partial failure are
  ambiguous.
- Cross-repository ACID assumption: rejected because Git/GitHub cannot provide
  it.
- Generic dry-run: rejected because side effects are often ambiguous.

## Verification

- Changed HEAD, remote OID, policy digest, or worktree fingerprint rejects
  apply.
- Retried completed steps are detected rather than repeated.
- Interrupted operations resume or compensate from the journal.

## Rollback

Use explicit compensation plans and new commits/PRs. Do not rewrite pushed
history by default.
