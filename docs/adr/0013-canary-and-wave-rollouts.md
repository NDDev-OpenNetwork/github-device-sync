# ADR 0013: Roll out immutable changes through canaries and waves

Status: Accepted

Date: 2026-07-11

## Context

One global change can affect many independent repositories and harness sessions.
Immediate estate-wide updates create unacceptable blast radius.

## Decision

Every bundle or portfolio-wide change uses:

1. immutable target version and digest;
2. representative canary repositories;
3. validation and agent eval gates;
4. bounded waves;
5. durable per-repository result and cursor;
6. pause on failure;
7. aggregate report;
8. explicit rollback target.

Each repository receives its own branch, commit, PR, checks, and operation
result. Mixed bundle versions are valid during rollout.

## Consequences

- Rollout is slower but failures are isolated.
- Compatibility across versions must be explicit.
- Rate-aware scheduling and deduplication are mandatory.

## Alternatives considered

- One mass commit: rejected because repositories have independent histories.
- Update all repositories immediately: rejected because of blast radius.
- Mutable stable channel without exact locks: rejected because rollback and
  audit become ambiguous.

## Verification

- Representative canaries cover visibility, owner, project/module, fork/source,
  stacks, and submodule consumers.
- A failed wave never advances automatically.
- Duplicate rollout requests reuse the same durable change key.
- Rollback rehearsal restores the previous verified bundle.

## Rollback

Pause new waves and apply an exact approved downgrade plan to the previous
immutable bundle. Preserve failed artifacts and raise the next corrective
release sequence.
