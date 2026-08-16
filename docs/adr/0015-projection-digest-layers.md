# ADR 0015: Separate projection body, file, and aggregate digests

Status: Accepted

Date: 2026-07-11

## Context

ADR 0007 requires generated Markdown to identify its output digest. Hashing a
file that literally contains its own full-file digest is self-referential and
cannot be implemented as an ordinary deterministic SHA-256 operation.

## Decision

Use three explicit digest layers:

1. The generated Markdown header `output-digest` is the SHA-256 digest of the
   rendered body after the generated header.
2. `projection.files[].digest` in `.gds/bundle.lock.yaml` is the SHA-256 digest
   of the complete generated file bytes, including the header.
3. `projection.output_digest` is the SHA-256 digest of the canonical ordered
   list of managed path/full-file-digest pairs.

`projection.input_digest` covers the typed repository anchor, compiled policy
digest, bundle metadata, and every template digest. Tracked output contains no
wall-clock timestamp.

The bundle lock does not list its own digest. Its schema and tracked repository
state protect it; including itself would recreate the same self-reference.

## Consequences

- Every digest has one non-recursive preimage.
- A body edit is detectable from both the header and lock.
- A metadata-header edit is detected by the lock's full-file digest.
- Aggregate drift is stable across platforms because paths are slash-normalized
  and ordered.

## Alternatives considered

- Placeholder normalization inside the full file: rejected because every
  validator would need an implicit rewrite rule before hashing.
- Omitting the digest from generated files: rejected because standalone files
  would lose local provenance evidence.
- Listing the lock's own digest: rejected as another self-reference.

## Verification

- Identical inputs produce byte-identical bodies, headers, locks, and aggregate
  digests.
- Header-only, body-only, compiled-policy, and lock edits are detected.
- Golden fixtures validate against the v1 bundle-lock schema.

## Rollback

Keep the previous immutable bundle and its documented digest algorithm. A
consumer must never reinterpret an existing lock with a different algorithm;
algorithm changes require a schema or bundle-contract version change.
