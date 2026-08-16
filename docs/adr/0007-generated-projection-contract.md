# ADR 0007: Generate standalone projections with provenance

Status: Accepted

Date: 2026-07-11

## Context

Current instruction and skill bridges use manual files and symlinks without an
immutable source/version/digest chain.

## Decision

Generated tracked projections include:

- generator identity;
- bundle version;
- source commit;
- input digest;
- output digest;
- canonical edit sources.

Generation is deterministic and timestamp-free for tracked outputs. Manual
changes are detected and never overwritten silently.

Symlinks are allowed only for verified local, same-boundary harness paths.
Standalone repository projections are generated files or immutable bundle
copies.

## Consequences

- Public repositories remain usable without private control-plane access.
- Drift becomes machine-detectable.
- Cross-platform packaging does not rely universally on symlinks.
- Golden fixtures and lock files are required.

## Alternatives considered

- Manual copies: rejected because of drift.
- Universal symlinks: rejected because of archive, platform, and
  security-boundary differences.
- Remote runtime includes: rejected because of mutability and offline failure.

## Verification

- Two generations from identical inputs are byte-identical.
- Manual-edit fixture returns PROJECTION_MANUALLY_MODIFIED.
- Public fixture contains no private source.

## Rollback

Regenerate from the previous immutable bundle. Preserve any unexpected manual
diff as evidence before replacement.
