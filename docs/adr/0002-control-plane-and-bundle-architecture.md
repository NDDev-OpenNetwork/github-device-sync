# ADR 0002: Canonical control plane and immutable bundle

Status: Accepted

Date: 2026-07-11

## Context

Reusable estate rules, skills, harness adapters, and generated copies are
currently split between github-device-sync and independent adapter repositories.
Direct global updates would create a large blast radius.

## Decision

github-device-sync is the canonical GDS control-plane repository.

It owns reusable implementation, schemas, policies, canonical skills, harness
profiles, generators, source register, and private estate configuration in
separate confidentiality boundaries.

Every distributable release is an immutable versioned bundle with:

- source commit;
- monotonic release sequence;
- content digests;
- schema and minimum CLI versions;
- reproducible build evidence;
- provenance attestation;
- SBOM for executable artifacts.

Managed repositories pin an exact bundle in .gds/bundle.lock.yaml. Rollout uses
canaries and waves.

## Consequences

- Reusable facts have one owner.
- Public repositories can consume standalone sanitized artifacts.
- Global change becomes slower but safer and reversible.
- The control plane needs release, trust, and rollout infrastructure.

## Alternatives considered

- Remote latest-file imports: rejected as mutable, network-dependent, and
  non-reproducible.
- Immediate update of every repository: rejected because of blast radius.
- Keep adapter sources permanently independent: rejected as a default authority
  model; selective package boundaries may remain after ADR review.

## Verification

- Bundle build is byte-reproducible.
- Consumer verifies digest, source repository, workflow identity, ref, and
  release sequence.
- Canary failure prevents the next wave.
- Public bundle scan finds no private estate data.

## Rollback

Pin repositories to the previously verified immutable bundle through an explicit
rollback plan. Do not mutate old releases or rewrite merged history.

## Related paths

- docs/architecture/README.md
- docs/runbooks/gds-migration-rollback.md
- artifacts/inventory/authority-conflicts.md (removed when the repository was
  published as public OSS; retained here as a record of what informed this
  decision)
