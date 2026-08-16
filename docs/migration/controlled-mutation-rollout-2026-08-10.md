# Controlled mutation rollout evidence

Status: canonical intent approved for review; no provider write is claimed by
this source change.

Date: 2026-08-10

## Scope

This migration changes two independent canonical gates:

- `rollout.mutation_mode` advances from `disabled` to `pull-request`;
- the generic non-fork, non-archived NDDev source selector advances from
  `observe-only` to `managed`.

The archive, fork, server, guild, personal, and Example-Media selectors remain
`observe-only`. The mutation capabilities still accept only `managed`
assignments and selected repositories.

## Preserved safety boundaries

The posture change does not authorize automatic mutation. Every provider write
continues to require all of the following:

1. an immutable exact-snapshot plan stored in the device state database;
2. a trusted signature over the exact plan digest and approval class;
3. one-shot device-local enablement for that plan;
4. fresh provider evidence and compare-and-swap validation;
5. a matching repository-scoped mutation capability and lifecycle;
6. a private device mutation runtime with no credential material in the estate;
7. `GDS_MUTATIONS_DISABLED=false` for the exact apply invocation;
8. durable apply and verify evidence.

Force, permission expansion, ruleset bypass, automatic merge, and unapproved
visibility changes remain forbidden by the mutation capability contract.

## Verification boundary

Repository validation, schema validation, projection reproducibility, memory
provenance, and the full PR-required test tier must pass on the exact source
commit before integration. Live GitHub permissions and writes are evidenced
only by subsequent signed operations against explicitly selected repositories;
they are not inferred from local fixture tests or from this migration.

## Rollback

Rollback is a reviewed source change that restores the NDDev source selector to
`observe-only` and `rollout.mutation_mode` to `disabled`, then regenerates all
derived projections from the rollback commit. The device kill switch can stop
new applies immediately without waiting for that repository change.
