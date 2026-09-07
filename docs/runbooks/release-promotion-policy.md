# GDS release promotion policy

## Scope

This runbook connects source verification, immutable publication, installation
and consumer adoption. The lifecycle commands are in
[release-lifecycle.md](release-lifecycle.md); the artifact shape is in
[the bundle contract](../contracts/bundle-release-v1.md). This document records
no private estate topology, deployed versions or current acceptance status.

Ordinary source integration follows the repository's selected development
policy. Release integrity remains required. A GitHub check is evidence for its
exact commit and outcome; source integration alone does not establish a release
or installed runtime.

## Release identity

The public GDS engine selects `release.mode: bundle`. A release binds:

- a clean exact source commit and permitted source ref;
- SemVer, channel and a monotonic release sequence;
- the supported Go toolchain and pinned dependency inputs;
- byte-identical builds and the exact six-file release directory;
- artifact digests, SPDX SBOM and Sigstore provenance;
- independently distributed consumer trust and offline verification material;
- signed active-seven harness evidence when the channel requires it.

The sequence must exceed the applicable consumer acceptance floor. A repository
transfer, workflow run number, version label or fresh tag does not reset that
floor. Conflicting published identities must not be overwritten.

## Channel requirements

| Channel | Source ref | Harness evidence | Meaning |
| --- | --- | --- | --- |
| canary | `refs/heads/main` or exact `refs/tags/gds-v<version>` | May be absent only as provisional | Candidate for bounded evaluation; no automatic promotion |
| stable | Exact `refs/tags/gds-v<version>` | Signed complete active-seven set | Nonprovisional release; consumer acceptance is still separate |
| frozen | Exact `refs/tags/gds-v<version>` | Signed complete active-seven set | Immutable identity retained under the consumer's rollback policy |

The builder verifies `antigravity-cli`, `claude-code`, `codex`, `cursor-cli`,
`grok-build`, `opencode` and `pi`, including aggregate/record signatures, anchored
producer/module identities, profile and bridge digests, and at most 72-hour
freshness. Individual profile flags do not waive stable/frozen aggregate proof.
`HARNESS_EVIDENCE_TRUST_POLICY_DIGEST` binds the workflow input to its independent
public trust policy. See `core/releasebuilder/harness_evidence.go` and
`core/harnessevidence` for the executable contract.

Publishing an immutable artifact does not install it. A consumer's rollout
policy decides eligible channels/rings, canary and rollback evidence, and later
promotion. It must not treat a provisional canary as a verified stable release
or mutate a published artifact to change its channel.

## Publication and installation

Checkpoint `A5` covers the exact tag, artifact, SBOM and publication identity.
Checkpoint `A6` covers an explicitly scoped canary rollout and rollback.
Use the existing owner authorization and signed operation contract; a previous
release or this runbook does not independently authorize a new provider write.

Before publication, require `scripts/validate_release.sh`, the builder's
independent rebuild and directory verification, and the target channel's signed
evidence. Build, attest and publish run on GitHub-hosted runners with separate
permissions. The repository is public; do not infer private attestation support
from OIDC permissions alone. [GitHub documents the availability boundary](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations).

Before installation, `gds release verify` must succeed against independent local
trust and exact offline materials. Plan/apply/verify then binds the target,
existing installation, acceptance ledger and approval. Runtime and rollback
acceptance are observed by the owning consumer; they are never inferred from a
release page or source commit.

## Module consumer pins

Consumer gitlinks are governed by each module's declared pin policy. GDS supports
`default-branch-commit` and explicit verified versioned-artifact transactions;
a global rule forbidding every raw main commit would contradict the first mode.
For a version-tag consumer, an advanced main branch does not satisfy the release
contract: verify the selected tag, source and required immutable assets before
advancing the gitlink. Do not change the pin policy to hide missing release
proof. The private estate owns its current pins and observations; this public
repository must not duplicate them in a version ledger.

## Rollback and refusal

Rollback is an explicit exception to monotonic installation order. It binds the
lower installed sequence and artifact digest, canonical install scope, bounded
reason, exact approval reference and expiry. Preserve the durable acceptance
floor and follow rollback with a new higher-sequence corrective release.

Refuse publication or promotion on dirty/unverified source, failed release
checks, nonreproducible artifacts, missing/mismatched attestations or SBOM,
stale/incomplete required harness evidence, conflicting sequence/tag identity,
unapproved provider writes, or a consumer target lacking its required trust and
acceptance evidence. Keep failures and missing evidence explicit.
