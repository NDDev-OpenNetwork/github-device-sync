# Legacy to GDS v1 migration

Status: planned.

## Inputs

- legacy .gitmodules topology;
- legacy tools/sync.sh container registry;
- device snapshots;
- direct metadata repository catalogs;
- provider observations;
- Phase 0 inventory evidence.

## Mapping

- GitHub numeric repository ID becomes provider.repository_id.
- A new stable repo-prefixed ULID becomes repository.id.
- GitHub owner/name becomes a mutable provider locator.
- Legacy container membership becomes portfolio classification evidence.
- Real gitlinks remain .gitmodules and index evidence.
- Project/module/fork meaning becomes explicit roles and relationships.
- Current branch, dirty state, local path, and access state do not enter the
  repository anchor.
- Device checkout lists move to local observed state.

## Reversibility

The migration does not remove or rewrite legacy files. Until parity is proven,
the v1 anchor and compiled inventory can be discarded without changing legacy
runtime behavior.

## Acceptance

- Every migrated fact has one declared source.
- Unknown values remain explicit.
- Stable IDs do not depend on owner/name or path.
- Round-trip fixtures preserve provider locators and Git topology.
- No secret or private provider observation enters public-safe schema fixtures.
