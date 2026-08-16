# ADR 0016: Bind release artifacts with a detached envelope

Status: Accepted

Date: 2026-07-11

## Context

The target design requires a release artifact digest in bundle metadata. A
digest stored inside the bytes it hashes is self-referential: changing the
digest changes the artifact and therefore changes the digest again.

The same problem already exists for generated projection files and was solved
by ADR 0015 with explicit digest layers.

## Decision

A GDS release unit has six deterministic files and five attested subjects:

1. `gds-bundle-vX.Y.Z.tar.gz` contains portable source files,
   `manifest.json`, and `checksums.txt`.
2. `release-envelope.json` is detached and binds the artifact digest, manifest
   digest, version, monotonic release sequence, channel, source commit,
   exact source ref, executable count, and expected attestation identity digest.
3. Detached `manifest.json`, `sbom.spdx.json`, and `bundle-trust.yaml` are
   byte-identical to their verified bundle members where applicable.
4. `SHA256SUMS` binds the exact other five-file set and is reproducible, but is
   not treated as an independent trust root.

The archive manifest lists only portable source members. It does not list
itself or the checksum file. The envelope does not live inside the archive.
The artifact, envelope, detached manifest, SBOM, and producer trust projection
receive provenance attestations. The artifact also receives an SPDX predicate.
All six files are immutable release outputs.

Consumer verification proceeds in this order:

1. validate the envelope schema;
2. verify the artifact digest from the envelope;
3. safely parse the bounded archive;
4. verify the manifest digest and manifest-to-envelope binding;
5. verify every member, mode, checksum, aggregate digest, and executable count;
6. verify attestation identity, SBOM evidence when executable content exists,
   offline verification material, and an independently pinned trusted-root
   digest;
7. enforce the persisted monotonic release-sequence floor.

## Consequences

- Every digest has a finite non-recursive preimage.
- Artifact tampering and envelope substitution are independently detectable.
- An attestation must cover the exact artifact and expected workflow identity;
  a checksum beside the artifact is insufficient.
- Release consumers must retain both objects and provenance evidence.
- A custom offline trusted root delivered beside an attestation is not trusted
  until its digest matches the independent local consumer policy.
- `source_ref` is explicit evidence and must resolve to the exact source commit;
  stable/frozen release refs must be exact version tags.

## Alternatives considered

- Put the artifact digest in `manifest.json`: rejected as self-referential.
- Hash the archive after replacing a digest placeholder: rejected because it
  creates an implicit normalization algorithm in every consumer.
- Trust only a detached checksum file: rejected because it does not bind
  source workflow identity, release sequence, or manifest semantics.

## Rollback

Existing release units are never reinterpreted. A format change requires a new
schema version. An operational downgrade requires an exact, expiring rollback
authorization and preserves the highest accepted release sequence.
