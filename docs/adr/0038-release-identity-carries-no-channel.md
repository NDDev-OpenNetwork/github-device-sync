# ADR 0038: Release identity carries no channel

Status: Accepted

Date: 2026-09-19

Supersedes: ADR 0016 (the envelope's `channel` binding only)

## Context

The release pipeline carried a `channel` field — `canary`, `stable`, `frozen`
for published artifacts and `development` for projections — and used it as
release identity and as a gate: stable and frozen required signed
active-seven harness evidence at build time, canary tolerated it as
provisional, and the consumer trust policy rejected envelopes whose channel
was not in `allowed_channels`.

That model conflated two different facts. A release's identity is established
by its exact source tag, version, monotonic sequence, artifact and manifest
digests, SBOM and attestation. Harness evidence is a fleet and runtime
signal: it proves the seven harness repositories were green on a specific
device within a bounded window, which is a property of where the release is
adopted, not of what was published. Gating publication on it pushed a private
signing key into the automatic release path and made the artifact's identity
depend on evidence that expires.

Channels also failed as a promotion mechanism: nothing promoted an artifact
between channels, so `stable` was only ever a label asserted at build time,
and every published artifact was in practice `canary`.

## Decision

Release documents carry no channel. Concretely:

1. `release_sequence` alone classifies a bundle: `0` is a development
   projection, `>= 1` is a release. No `development` channel exists.
2. Every release builds from the exact `refs/tags/gds-v<version>` tag. The
   release workflow resolves the next version and sequence from the latest
   published envelope, creates that tag, and only then builds from it.
3. `channel` fields remain as optional decode-compatible members in schemas
   and types that must read documents produced while the field existed, and
   in the signed harness-evidence manifest payload where removing it would
   change re-digests of already-signed manifests. They are never written by
   current producers and never gate anything.
4. `allowed_channels` is removed from the required trust-policy surface; the
   consumer check applies only to legacy envelopes that still carry a
   channel.
5. Harness evidence remains a separately produced, separately verified
   estate signal (module release evidence, runtime validation). It is not an
   input to `bundle.Build` or the release builder.
6. Rollout rings keep their cohort names (`canary`, `representative`,
   `early`, `general`): a deployment wave label is not a release channel and
   ADR 0013 is unaffected.

## Consequences

- The automatic release path needs no device signing key; release integrity
  is tag + sequence + reproducible artifact + attestation.
- Legacy v1 artifacts decode and verify exactly as before; their channel is
  still checked against a consumer policy that lists it.
- Version bumps never reclassify a development lock, because classification
  is the sequence, not a version string.

## Alternatives considered

- Keep channels but stop gating on evidence: rejected because a label that
  carries no mechanism is worse than no label — it implied promotion semantics
  that did not exist.
- Promote artifacts between channels by rewriting envelopes: rejected because
  it mutates published identity, which the immutability contract forbids.

## Verification

- `bundle.Build` and `gds-release-builder` reject every source ref other than
  the exact version tag and every release sequence below 1.
- The workflow contract test forbids channel inputs and harness-evidence
  inputs on the release workflow and forbids candidate checkout in the
  privileged resolve job.
- Schema fixtures: current valid documents omit channel; a trust policy that
  still lists `allowed_channels` validates; an envelope with a channel value
  outside the legacy enum is still rejected.

## Rollback

Reintroducing a channel would require a new ADR, schema version negotiation
for the restored required field, and a signing-key story for the evidence
gate. The decode-compatible fields can be dropped in a future schema version
once no consumer needs to read a pre-0038 document.
