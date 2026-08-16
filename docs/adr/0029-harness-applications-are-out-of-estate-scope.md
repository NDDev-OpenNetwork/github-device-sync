# ADR 0029: Keep harness application versions out of estate scope

Status: Accepted

Date: 2026-07-26

## Context

`estate/runtime-dependencies.yaml` registered the three harness applications —
`nddev-codex-app`, `nddev-zcode-app`, `nddev-claude-app` — as runtime-clone
dependencies with a `pinned_sha`, an `available_head`, and a `reconciliation`
state. Two of them were cross-checked against the `macos-ubuntu-bootstrap`
in-tree contract by `core/validation/runtime_dependencies.go`; the third was
declared for visibility only, with its cross-check deliberately skipped.

That registration made this estate a second version authority for products it
does not own. Each harness application is an independent product with its own
repository, release lifecycle, and consumers. The estate does not build them,
release them, or decide their versions.

The cost was visible: the registry's `available_head` for all three had silently
fallen two commits behind their real heads while still asserting
`reconciliation: current`. Nothing caught it, because the validator compares
`pinned_sha` against the consuming repository's contract, not against the live
remote. The registry was reporting confidence it did not have.

The registration also bought nothing. Whether a consumer is on the right commit
is already enforced where it matters — by that consumer's own contract, checked
in its own repository.

## Decision

1. The three harness applications are removed from
   `estate/runtime-dependencies.yaml`. The registry declares `dependencies: []`.
2. This estate neither tracks nor verifies harness application versions. Their
   consumption is governed entirely by the consuming repository's own contract.
3. The registry, its schema, and its validator remain. They stay the single
   place to declare a genuine non-submodule runtime module, and the drift
   cross-check stays available for one that qualifies.
4. Consumption paths outside this estate — for example the `example-harnesses`
   gitlinks, validated by that repository's own
   `scripts/validate_control_plane.py` against its `config/repositories.json` —
   are not mirrored here.

## Consequences

- No stale `available_head` can accumulate for a product this estate does not
  release, because none is recorded.
- `GDS_RUNTIME_DEPENDENCY_PIN_DRIFT` and
  `GDS_RUNTIME_DEPENDENCY_RECONCILIATION_INCONSISTENT` no longer fire on harness
  applications; both remain reachable for a future registered module.
- Advancing a harness pin in a consuming repository is that repository's change
  alone and needs no estate edit.
- The estate no longer offers a single place to read which harness commit a
  device runs. That answer now comes from the consuming contract, which is the
  only place that was ever authoritative.

## Rollback

Re-add the entries with their real current `pinned_sha` and `available_head`.
Nothing else changes; the validator and schema are unmodified.
