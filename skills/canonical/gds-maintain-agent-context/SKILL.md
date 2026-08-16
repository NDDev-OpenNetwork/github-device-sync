---
name: gds-maintain-agent-context
description: Use this skill when verified repository facts changed and generated AGENTS, harness projections, or provenance-bearing Serena memories may be stale. Regenerate only affected context from canonical sources and validate drift. Do not use it for speculative documentation rewrites or estate-wide bundle rollout.
---

# Contract

Refresh derived agent context from verified canonical sources without creating
a second policy authority.

## Use when

- Repository commands, architecture, boundaries, or policy facts changed.
- Generated projections or derived memories report drift.

## Do not use when

- Facts are speculative or unverified.
- The requested change belongs in global policy or a new bundle release.

## Inputs

- Changed canonical paths and repository identity.
- Current compiled policy and bundle lock.
- `product` in `.gds/repository.yaml`: purpose, capabilities and the
  `change → path` entrypoints the brief leads with. A repository that declares
  none renders a brief with no product facts, which is the state this skill
  exists to correct.

## Preconditions

1. Identify the canonical owner of each changed fact.
2. Preserve manual diffs instead of silently overwriting them.

## Workflow

1. Run `gds generate repository --check --json`.
2. Determine which projections or memories are affected.
3. Stage the canonical source edit. The bundle identity is a content digest of
   the staged sources, so the edit and its regenerated output belong in one
   commit; committing first is neither required nor useful.
4. Plan and apply:

   ```bash
   gds --json generate repository --plan --device-id <device> --session-id <s>
   gds --json generate repository --apply <plan-id> --device-id <device> --session-id <s>
   ```

   A repository-local projection write needs no signed approval and no one-shot
   enablement. Those remain required for harness adapters, which write outside
   the repository, and for provider mutations.
5. Re-stamp affected memories with `gds memory generate <name>`, then run
   projection, memory provenance, visibility, and reproducibility checks.

## Stop conditions

Stop on manual projection drift, missing provenance, private-to-public flow,
stale bundle identity, or unresolved canonical ownership.

## Verification

Regenerate twice and require byte-identical output with zero unexplained drift.

## Output

Return canonical inputs, generated paths, digests, preserved manual diffs,
tests run, and evidence still not proven.

## References

No additional runtime reference is required; projection metadata names its sources.
