# ADR 0006: Separate desired, observed, generated, and derived information

Status: Accepted

Date: 2026-07-11

## Context

The legacy system repeats provider state and topology across shell data, Git
configuration, catalogs, instructions, tests, device snapshots, and memories.

## Decision

Use five information classes:

1. reusable implementation in the control-plane source and released bundle;
2. desired estate configuration in estate/;
3. repository-owned facts in .gds/repository.yaml;
4. observed runtime/provider state in the controller state store;
5. generated or derived agent knowledge with provenance.

Each mutable fact has one canonical owner. Generated files and Serena memories
never override code, manifests, or verified provider state.

## Consequences

- Duplicate ledgers become detectable and removable.
- Provider observations can expire without rewriting desired configuration.
- Repository-specific facts remain available offline.
- The compiler must record provenance.

## Alternatives considered

- One large estate YAML: rejected because it duplicates discoverable facts and
  mixes confidentiality boundaries.
- Commit all observed state: rejected because it creates stale churn.
- Treat memory as authority: rejected because memory is derived.

## Verification

- An authority matrix maps every schema field to one owner.
- Conflicting sources fail compilation.
- Stale provider observations remain marked stale or unknown.

## Rollback

Keep legacy catalogs read-only during migration. Do not remove them until
compiled inventory and provider reconciliation agree.
