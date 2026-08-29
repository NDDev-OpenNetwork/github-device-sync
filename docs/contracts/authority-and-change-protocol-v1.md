# Authority and change protocol v1

Status: current.

This is the live half of what used to be `docs/migration/gds-completion-plan.md`.
That file was the authority for a migration order that is finished, and it is
deleted along with the rest of the completed-phase record; git holds it. The
rules below are not migration steps — they govern every change made now — so
they live here instead.

## Canonical owner per concern

A fact has exactly one owner. Read it there; do not copy it elsewhere.

| Concern | Canonical owner |
|---|---|
| Reusable runtime behavior | `core/`, `policies/`, `skills/`, `harnesses/`, `templates/` |
| Repository-owned facts | `.gds/repository.yaml` in each Git boundary |
| Observed state | controller / local state store |
| Generated context | compiler output plus `.gds/bundle.lock.yaml` |
| Derived durable knowledge | provenance-bearing Serena memories |
| Decisions and their reasons | `docs/adr/` |
| Contracts between surfaces | `docs/contracts/` |
| Operator procedures | `docs/runbooks/` |

A missing capability extends its canonical package and CLI contract. It does
not create a parallel script, policy file, skill, or hand-maintained
projection. Nothing is reopened through a second implementation path.

## Status vocabulary

Only these states are used, and the first two are never synonyms for
`accepted`:

- `implemented-local` — code and deterministic local tests pass;
- `foundation-only` — reusable primitives exist but the end-to-end workflow is
  absent or disabled;
- `not-proven` — required runtime or external evidence was not obtained;
- `missing` — no implementation satisfies the contract;
- `conflicting` — two surfaces disagree or cross an ownership boundary;
- `blocked` — an explicit prerequisite or approval prevents safe progress;
- `accepted` — every required gate for that scope passed with durable evidence.

## Change protocol

Every change follows this order:

1. Resolve current scope, Git boundaries, applicable policies, source
   freshness, and dirty/untracked state.
2. Update the canonical owner only.
3. Add or update deterministic tests before enabling a mutation path.
4. Generate candidates; never edit generated files directly.
5. Review semantic and security diffs, including public/private flow.
6. Run the smallest applicable gate, then the complete gate.
7. Update contracts, ADRs, runbooks, changelog and source register only when
   verified facts changed.
8. Regenerate Serena memories from committed sources and validate provenance.
9. Commit independently in each Git boundary. For dependency changes, commit
   and publish the dependency first, then update the consumer pin or gitlink.
10. Push, verify remote OIDs and hosted checks, and journal external results.
11. Require a clean verified boundary before advancing dependent work.

Documentation never claims more than the evidence supports. Evidence records
the actual command, version, environment, result, failures, and unproven
checks.
