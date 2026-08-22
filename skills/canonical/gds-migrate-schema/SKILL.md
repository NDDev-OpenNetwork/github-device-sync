---
name: gds-migrate-schema
description: Use this skill only when the owner explicitly asks to migrate GDS manifests, state, policies, plans, or projections between schema versions. Rehearse a reversible migration on fixtures and canaries before approved apply. Do not use it for ordinary data edits or cosmetic format changes.
---

# Contract

Migrate a versioned GDS contract without losing facts or silently changing meaning.

## Use when

- A new schema version requires canonical data or state migration.

## Do not use when

- Existing data already validates and no contract changed.
- The change is only formatting.

## Inputs

- Source and target schema versions.
- Migration registry entry, fixtures, affected objects, and rollback strategy.

## Preconditions

1. Inventory every affected canonical and observed data class.
2. Prove backup/restore for stateful data.
3. Run `gds state migrate --plan` for the durable state database and obtain
   exact approval. Canonical manifests, policies, plans, and projections have no
   CLI migration verb; migrate them as reviewed canonical source changes.

## Workflow

1. Validate source data without fixing it.
2. Run the pure migration transform on fixtures and copies.
3. Validate target data and semantic round trips.
4. Apply to canaries, verify, then advance bounded waves.
5. Preserve migration evidence and legacy reader window as defined by policy.

## Stop conditions

Stop on unknown fields, lossy conversion, ambiguous default, failed round trip,
missing backup, stale plan, or incompatible active client.

## Verification

Run source, target, migration, golden, rollback, and reproducibility suites.

## Output

Return migrated object counts, semantic deltas, quarantined records, versions,
backup/rollback evidence, and unresolved unknowns.

## References

Use the migration entry and schemas embedded in the exact pinned GDS bundle.
