# ADR 0008: Deterministic policy precedence and provenance

Status: Accepted

Date: 2026-07-11

## Context

Current rules are distributed through shell conditions and Markdown. Implicit
specificity cannot safely resolve conflicts at estate scale.

## Decision

Policies apply in fixed order:

base, owner, portfolio, role, stack, lifecycle, repository.

Merge behavior is schema-defined:

- scalar: later permitted tier wins;
- map: deep merge only when declared mergeable;
- list: replace by default;
- list changes: explicit append/remove;
- same-tier conflicts: error;
- unknown field, missing profile, or cycle: error.

Every effective leaf records provenance. Security monotonic fields cannot be
weakened without an explicit expiring exception.

## Consequences

- Effective behavior is explainable and reproducible.
- Repository overrides remain sparse.
- Schema design must mark merge semantics explicitly.

## Alternatives considered

- Last file wins: rejected as order-sensitive and opaque.
- YAML anchors/merge keys: rejected because parser support and semantics vary.
- Heuristic specificity: rejected as non-deterministic.

## Verification

- Golden policy fixtures include provenance.
- Equal-tier conflicts, cycles, unknown fields, and forbidden weakening fail.
- Expired exceptions stop applying automatically.

## Rollback

Keep the previous compiled policy and bundle lock. A failed compiler version
cannot modify repositories.
