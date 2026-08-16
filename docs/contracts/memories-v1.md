# GDS Serena memory v1 contract

Status: Phase 11 provenance validation implemented; current memories are
verified against committed source inputs.

## Role

Serena memories are compact derived knowledge loaded on demand. They never own
desired state, runtime Git status, authorization, secrets, operation plans, or
external mutation decisions.

Canonical machine facts remain in code, schemas, manifests, policies, provider
observations, and tests.

## Paths and names

Tracked memories live in:

```text
.serena/memories/<semantic-kebab-name>.md
```

Numeric taxonomies such as `CORE-01-*` and `FLOW-02-*` are forbidden because
they add routing noise without describing intent.

## Required metadata

Each memory starts with schema-validated YAML frontmatter containing:

- repository scope ID;
- status and visibility;
- source commit and whether the sources are committed or working-tree state;
- deterministic digest of sorted source paths and file digests;
- generator and bundle version;
- verification timestamp;
- refresh triggers;
- exact repository-relative source paths.

`verified` is valid only with `source_state: committed`. During an uncommitted
migration, `generated-unverified` plus a matching source digest is the honest
state.

## Required body

Every memory has one title and these sections:

```text
## Purpose
## Invariants
## Sources
## Refresh
```

Only stable, non-obvious facts belong in the body.

## Deterministic validation

```bash
gds validate memories --json
```

The validator:

- enumerates only bounded regular Markdown files;
- enforces semantic filenames and metadata schema;
- confines sources to the repository and rejects symlinks;
- hashes exact current source bytes;
- detects missing sources and stale digests;
- rejects verified status for working-tree sources;
- checks body/frontmatter source-list equivalence
  (`GDS_MEMORY_SOURCE_LIST_DIVERGENCE`): the backtick-quoted paths in the
  body `## Sources` section must form the same set as the frontmatter
  `sources:` list, so a source added to one but not the other is caught;
- never rewrites a memory.

### What the anchor governs

The repository's own anchor decides how much of the above applies to its tree.
`schemas/v1/repository.schema.json` makes `agent.serena.enabled` and
`agent.serena.provenance_required` required booleans, so writing them is a
contract statement rather than an omission.

- `enabled: false` -- the tree owes no memory set. An absent or empty memory
  root is the declared state and reports nothing. Memories *present* under that
  declaration are reported (`GDS_MEMORY_DISABLED_STATE_PRESENT`): somebody wrote
  agent state into a repository that says it keeps none, and the next reader has
  no contract telling them what it is or who maintains it.
- `enabled: true, provenance_required: false` -- structure, semantic names and
  the body contract are enforced; digest binding is not. Reporting a stale
  digest here would assert a contract the repository declined. An empty or
  absent memory root is not reported either: `GDS_MEMORY_SET_EMPTY` says a
  claimed assurance is missing, and a repository that keeps memories without
  binding them to sources has not promised to keep any.
- `enabled: true, provenance_required: true` -- everything above, including
  `GDS_MEMORY_SET_EMPTY`. This control plane declares this.

An anchor that cannot be read falls back to the strictest reading. The opt-out
has to be stated to take effect; inferring it from a file that failed to parse
would let a broken anchor disable a gate silently, which is the failure this
rule exists to remove.

## Regeneration

Editing any file listed in a memory's `sources:` changes its `source_digest`,
so `gds validate memories` (and the `core/memory` and `core/cli` tests) fail
until the memory is re-synced. This is expected drift, not a defect. The sync is
commit-first:

1. Commit the source change. `gds memory generate <name>` resolves the source
   commit from the committed history and refuses a dirty tree with
   `GDS_MEMORY_COMMITTED_SOURCE_NOT_PROVEN`.
2. Run `gds memory generate <name>`. It preserves the authored body, recomputes
   the digest and source commit, and emits `generated-unverified` whenever the
   commit or digest changed. Apply the new `source_commit`, `source_digest`, and
   a fresh `verified_at` to the frontmatter.
3. Restore `status: verified` only as a deliberate assertion that the body still
   describes the current source; add any new stable invariant to the body while
   re-reading it. The generator never promotes status on its own.
4. Commit the memory update on its own, conventionally as a `docs(memory):`
   change separate from the source commit.

## Current semantic set

```text
core-bundle-rollout
core-context-resolution
core-estate-layout
core-github-controller
core-harness-adapters
core-integrated-assurance
core-operation-safety
core-policy-projection
```

The former numeric memories were retired only after these replacements covered
their still-valid knowledge. Legacy four-level/container claims that conflict
with the typed graph model were not copied forward.
