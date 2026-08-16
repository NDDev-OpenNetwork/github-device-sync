# GDS immutable bundle release v1 contract

Status: local build, verification, installation, rollback, and removal contracts
implemented. The repository is private and owned by the example-org
organization, so artifact attestation is available. Hosted attestation and
publication are proven: `gds-v0.1.0` (source commit `bace996`) was built,
attested, and published on 2026-07-24T10:11:01Z. Consumer adoption, canary
rollout, and Linux consumer execution remain NOT_PROVEN (see "Not proven").

## Portable source boundary

The release builder includes only registered portable schemas, policies,
canonical skills, harness profiles, safe templates, Codex plugin packages, and
the three GDS executables. It excludes `estate/`, observed state, device
configuration, Git metadata, runtime credentials, and private inventory.

Every member is bounded, slash-normalized, and regular. Symlinks, traversal,
oversized members, duplicate paths, private user paths, high-confidence secret
markers, and unexpected executable content fail closed.

## Reproducible release unit

`gds-release-builder` requires a fully tracked clean Git worktree, exact source
ref resolving to `HEAD`, Go `1.26.5`, read-only modules, CGO disabled, portable
CPU baselines, and an isolated build environment without ambient credentials or
Git configuration. Stable and frozen channels require
`refs/tags/gds-v<version>`; canary accepts only `refs/heads/main` or that exact
tag.

The builder obtains the same process-wide `core/gitauthority` used by runtime
Git providers before source inspection and retains it through private fetch,
tree comparison, and archive creation. Caller `PATH`, repository-selection,
object-alternate, Git-config, helper, and loader variables cannot select either
the executable or repository authority. The resolved executable identity is
rechecked around every command. Its exact version and SHA-256 digest are
recorded as an SPDX creator in the SBOM that is embedded in the bundle and used
as the hosted SBOM attestation predicate. The default Go executable is resolved
from the running builder's `GOROOT` when that identity is available, and child
`PATH` is the fixed system path. A materialized builder whose trimpath build has
no runtime `GOROOT` requires `--go-binary`; every explicit Go override must be
an absolute executable regular file. The hosted workflow derives that absolute
path from the exact setup-go toolchain before invoking the builder.

After inspection, the builder bounds the exact commit object and tracked tree
before fetching the commit into a private temporary object database. It then
streams a size-limited archive into a bounded, regular-file-only, read-only
source snapshot, so commit-message and `export-subst` expansion cannot bypass
the resource cap. All release-content reads, module inspection, plugin
packaging, and binary and bundle builds use that snapshot; later
caller-worktree changes cannot alter the release bytes bound to the inspected
commit. Unsafe archive paths, symlinks, non-regular entries, unsupported modes,
and oversized streams fail closed, and all temporary source material is
removed on every return path.

The builder performs two independent binary builds and two archive assemblies.
The materialized release directory contains exactly:

```text
gds-bundle-v<version>.tar.gz
release-envelope.json
manifest.json
sbom.spdx.json
bundle-trust.yaml
SHA256SUMS
```

Identical source, version, sequence, channel, toolchain, and ref produce
byte-identical output. Tracked outputs contain no wall-clock timestamp.

Publication of that directory is atomic and identity-checked. Staged files are
renamed onto the destination, and any failure after that commit point rolls the
publication back by removing only the exact renamed identity. A failed build
therefore leaves no unverified directory that could be mistaken for a release
output or block a retry, and a concurrently replaced destination is reported
instead of deleted.

The archive contains:

- portable canonical source;
- `gds`, `gds-controller`, and `gds-codex-runtime-driver` for darwin/linux and
  amd64/arm64;
- standalone `gds-core`, `gds-estate-admin`, and `gds-module` Codex packages;
- SPDX 2.3 SBOM;
- the producer trust-policy projection;
- deterministic internal manifest and checksums.

`core/bundle/release_contract.go` owns the required target and executable
matrix. The builder and consumer both use that contract. A release is rejected
when any target/command pair is absent, has non-executable metadata, or when an
unregistered `bin/` member is present; host usability is checked separately
from full portable-bundle completeness.

## Detached identity

ADR 0016 defines the non-self-referential layers. The detached envelope binds
artifact digest, manifest digest, version, monotonic sequence, channel, source
commit, exact source ref, executable count, and expected attestation identity.
The six-file directory verifier rejects any missing, extra, symlinked, renamed,
oversized, or digest-mismatched member.

## Consumer trust and offline evidence

The independent local `bundle-trust.yaml` binds:

- source owner and repository;
- one exact workflow and allowed refs;
- channels and minimum release sequence;
- mandatory provenance and executable SBOM attestations;
- mandatory offline evidence;
- the exact SHA-256 digest of `trusted-root.jsonl`;
- the exact GitHub CLI version and extracted executable digest for every
  supported release target (`darwin`/`linux` × `amd64`/`arm64`).

The trusted-root digest is obtained and reviewed out of band. A trusted root
delivered beside an attestation is not self-authenticating. Producer CI and the
consumer both compare it with the independent local pin before using it.

Offline verification reads the resolved regular GitHub CLI executable once,
checks its target-specific digest and version, and invokes only a private copy
of those verified bytes in an isolated HOME/config environment with no GitHub
token. It performs six bounded checks: provenance for the artifact, envelope,
manifest, SBOM, and trust projection, plus the SPDX predicate for the artifact.
Every returned statement must bind the exact subject digest, repository,
workflow, source commit, and source ref.

The six release members and consumer trust policy are captured through stable
file descriptors into a private snapshot; verifier bytes are retained in
memory and copied privately for each invocation. Verification and installation
read the same captured inputs and recheck every stored digest, so replacement
of the caller paths cannot change the accepted install candidate.

The hosted builder writes beneath a private `$RUNNER_TEMP` parent, never beneath
the checked-out source root. Provenance checksums keep stable `release/`
subject names while attest and upload actions consume the absolute temporary
paths supported by their contracts.

## Installation contract

Each accepted release is materialized atomically under:

```text
<install-root>/releases/<sequence>--<version>--<digest-prefix>/
├── payload/
├── release/
├── evidence/
├── consumer-trust.yaml
└── install-record.json
```

The install root is canonicalized through its real parent. The `current`
pointer is a relative local symlink and is replaced atomically. Installed files,
modes, sizes, evidence, trust policy, payload, and record must match the stored
candidate exactly. Manual additions, symlinks, or byte drift block activation
and verification.

Every cooperative `current` mutation is serialized by one exclusive advisory
lock file opened beneath the canonical install root without following a final
symlink. The lock is independent of the selected state database, shared across
trust domains because `current` is root-wide, released by descriptor close or
process exit, and supported on the release targets (darwin and linux). The
final active-state compare, candidate re-verification, pointer rename and
directory sync, post-rename candidate re-verification, and acceptance-ledger
write execute while that lock is held.
An exact materialized or already-active candidate is idempotent so a separately
approved retry can reconcile a crash after pointer replacement. A reported
ledger failure restores the previously observed pointer while the lock remains
held when recovery is still possible.

The filesystem pointer and SQLite acceptance ledger are not one cross-resource
transaction. Crash atomicity across those resources is NOT_PROVEN: after an
interruption, either the prior pointer remains or the exact verified candidate
is active and can be reconciled idempotently. The lock is a cooperative,
single-host filesystem boundary; non-cooperating writers and network filesystem
lock semantics are outside this contract.

The acceptance ledger permanently binds each sequence to one artifact digest
and keeps the highest accepted sequence. Upgrade requires a new higher
sequence. Rollback requires an already installed lower release and one exact,
unexpired authorization binding target sequence, artifact digest, canonical
installation scope, rollout ID, reason, and approval reference. Removing a
currently active lower rollback release is permitted only when its exact digest
already exists in the acceptance ledger; this does not lower the stored floor.

## CLI boundary

Read-only evidence verification:

```text
gds release verify \
  --release-directory <release> \
  --evidence-directory <offline-evidence> \
  --trust-policy <local-trust> \
  --state-path <state.db>
```

Local lifecycle commands use exactly one transaction mode:

```text
gds release install  --plan|--apply <plan-id>|--verify <operation-id>
gds release upgrade  --plan|--apply <plan-id>|--verify <operation-id>
gds release rollback --plan|--apply <plan-id>|--verify <operation-id>
gds release remove   --plan|--apply <plan-id>|--verify <operation-id>
```

Planning is side-effect-free. Apply requires the stored plan, exact current
inputs, current repository/policy/source preconditions, device/session identity,
and approval. Verification reconstructs the candidate from the stored plan and
installed immutable copy; caller-supplied release paths cannot alter it.

`gds release candidate` remains a read-only in-memory design check. The trusted
materialized builder is `gds-release-builder`; neither command publishes.

## Release gate

`scripts/validate_release.sh` requires:

- exact Go release toolchain and module graph;
- format, tidy-diff, module verification, vet, unit, race, and cross-build gates;
- schema/fixture, repository, policy, projection, memory, plugin, and skill checks;
- verified runtime context and exact generated-projection equality against
  committed canonical sources;
- static and recorded runtime harness evidence;
- source freshness without overdue, blocked, or unproven claims;
- visibility, absolute-path, public-artifact, and reproducibility checks.

The GitHub workflow checks out complete commit history and runs these gates
before building, attesting, or uploading. It is manual, SHA-pinned, globally
serialized, permission-minimal, and does not create a tag, merge, or rollout. On
a tag-dispatched run it publishes one GitHub Release for the already-existing
`refs/tags/gds-v<version>` ref (`gh release create --verify-tag`) and attaches
the same six governance-verified release files; a branch-dispatched canary run
keeps its bundle as the workflow artifact only.

Every release job runs on GitHub-hosted runners. The consumer places no
constraint on `runner_environment`: what it verifies is the signer identity —
repository, reusable-workflow path, source commit and ref, against the estate's
own trusted root — and the hosting provider adds nothing to that. The earlier
rule that releases must be GitHub-hosted made the estate's own builds
un-installable and has been removed.

The control-plane repository is private and owned by the example-org
organization, so the hosted `actions/attest` steps are an available release
path. An ad hoc signing key or a workflow that silently omits attestations is
still not an accepted fallback. A change of repository visibility or ownership
re-opens the trust-boundary decision before the next dispatch.

## Proven

- one live GitHub Actions run of the release workflow per channel: run
  `30046936069` (canary, `refs/heads/main`, 2026-07-23) and run `30064955206`
  (stable, `refs/tags/gds-v0.1.0`, 2026-07-24);
- hosted `actions/attest` provenance over the five checksummed release files
  and an SPDX SBOM attestation over the bundle artifact;
- external artifact publication: the six-file release directory is attached to
  the `gds-v0.1.0` GitHub Release. From the workflow revision that followed that
  tag, publication also attaches the offline evidence directory to the same
  release, so a release and its evidence are one durable artifact set.

## Not proven

- durable retention of the offline evidence directory for `gds-v0.1.0`
  specifically: that tag predates the evidence attachment, so its evidence
  exists only as the producing run's workflow artifact under a 30-day retention
  window. Later releases attach it. Because the release and its evidence are
  then published together, a consumer must download into a staging directory
  and split the assets into the two consumer inputs: the release directory
  accepts exactly six entries and the evidence directory exactly the three
  required inputs (`docs/runbooks/seed-clean-device.md`, step 2b). Auxiliary
  result JSON published alongside the evidence belongs to neither input;
- GitHub-enforced release immutability: the published release is not marked
  with GitHub's immutable-releases flag, so release-asset immutability rests on
  this contract's digest/envelope/sequence binding, not on the provider;
- consumer-side verification of the published artifact on a clean device;
- Linux consumer execution of install/upgrade/rollback/remove;
- canary repository adoption, merge, rollback, or broad rollout.

Those remain external mutation boundaries and require separate exact approval.
