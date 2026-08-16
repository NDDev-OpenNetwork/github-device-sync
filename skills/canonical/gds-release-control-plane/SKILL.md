---
name: gds-release-control-plane
description: Use this skill only when the owner explicitly asks to release a verified GDS CLI, plugin, or immutable bundle. Enforce reproducible build, monotonic release sequence, checksums, provenance, SBOM policy, and exact artifact identity. Do not use it to develop features or roll out the release to repositories.
disable-model-invocation: true
---

# Contract

Publish one immutable, verifiable GDS release unit after all release gates pass.

## Use when

- A prepared control-plane version is ready for approved publication.

## Do not use when

- Implementation, migration, or runtime evidence is incomplete.
- The request is only to roll out an existing release.

## Inputs

- Exact source commit, version, release sequence, channel, and artifact set.
- Release approval and trust policy.

## Preconditions

1. Require clean canonical source and complete release evidence.
2. Verify the exact supported toolchain and dependency locks.
3. Run `gds release candidate --version <version> --sequence <sequence> --channel <channel>`
   for the side-effect-free candidate and obtain approval for publication. The
   CLI has no publish verb; publication runs through the `gds-release-bundle`
   workflow.

## Workflow

1. Build twice in isolated environments and compare digests.
2. Produce manifest, checksums, provenance, and required SBOM.
3. Verify consumer-side identity and anti-rollback policy.
4. Publish immutable artifacts and tags without rewriting them.
5. Verify download and offline verification material where supported.

## Stop conditions

Stop on dirty source, test failure, vulnerable release toolchain, digest drift,
attestation mismatch, reused sequence, missing approval, or mutable target.

## Verification

Run `gds release verify --release-directory <directory> --evidence-directory
<directory> --trust-policy <policy> --json` against published artifacts.

## Output

Return version, sequence, source commit, artifact digests, attestation identity,
SBOM state, release location, and rollout approval still required.

## References

Use the release plan, trust policy, and pinned bundle sources for this release.
