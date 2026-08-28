# GDS release promotion policy

## Scope

This runbook consolidates the release, channel-promotion, and consumer-pin
governance that is otherwise spread across `release-lifecycle.md`, the
`gds-release-control-plane` skill, and the completion-plan checkpoint table. It
defines *when* an artifact may advance from build to a channel to a consumer
pin. It does not restate the lifecycle commands (see `release-lifecycle.md`) and
authorizes no external mutation.

## Status

Policy defined; **the first external immutable release has been executed.**
`gds-v0.1.0` (source commit `bace996`) was built, attested, and published on
2026-07-24T10:11:01Z by `.github/workflows/release-bundle.yml` through the
`release` gate, with keyless Sigstore SLSA build provenance and an SBOM
attestation over the six-file release directory. Artifact attestation is
available because the repository is private (ADR 0033) but keyless Sigstore
attestation works for private repositories as long as the workflow holds
`id-token: write`, and the repository is owned by the example-org
organization. Harness runtime proof is delegated out of the release gate (every
`harnesses/*/profile.yaml` sets `runtime_tests.required: false`), so the
`codex`/`zcode` records being `not-proven` did not block publication.

Still `NOT_PROVEN` and therefore still gating downstream promotion: harness
runtime evidence itself (`C6`), Linux consumer rehearsal, canary and estate
rollout adoption (`C11`/`C12`), live GitHub App evidence, and restore/recovery
rehearsal. Publishing an artifact is not promoting it. Do not weaken any gate to
work around these boundaries.

Authority: `docs/migration/gds-completion-plan.md`. Release mechanics:
`docs/runbooks/release-lifecycle.md`. Bundle contract:
`docs/contracts/bundle-release-v1.md`.

## Release identity

`release.mode: bundle` (`.gds/repository.yaml`). A release identity is coherent
only when all of these agree and are recorded together:

- source commit (fully tracked clean worktree, reproducible `go1.26.7` build);
- monotonic release sequence (the anti-rollback floor);
- artifact digests over the exact six-file release directory;
- SPDX SBOM and Sigstore provenance in the offline evidence directory;
- SemVer label and changelog entry;
- selected-harness runtime evidence for the target device set, for every profile
  that declares `runtime_tests.required: true`.

A version file, a Git tag, and a changelog line are not independently
authoritative. None of them promotes an artifact; the recorded release identity
does.

## Channels and promotion order

Channels advance in one direction only; an artifact never skips a stage:

1. **build** — reproduced byte-identical in two isolated environments; digests
   compared. Not installable.
2. **canary** — installed on a canary control-plane device
   (`rollout_ring: canary-control-plane`). Requires the read-only
   `gds release verify` = `success` and one proven rollback to the prior
   immutable bundle on that canary (stage `C11`).
3. **stable** — the estate default channel (`default_bundle_channel: stable`).
   Promotion requires the canary acceptance above plus green required checks.
4. **frozen** — an accepted identity retained for rollback targeting; never
   mutated or rewritten.

`rollout.mutation_mode: pull-request` and `default_ring: standard` are the
current controlled posture. Only managed NDDev source repositories are
eligible, and this policy does not bypass exact signed approval, one-shot
enablement, fresh provider evidence, mutation-capability scope, or the device
kill switch. All observe-only assignments remain non-mutable.

## Approval checkpoints

Two checkpoints gate the externally-visible transitions (completion plan §9):

- **A5** — tag / release / artifact / SBOM / distribution. Required before any
  hosted publication.
- **A6** — canary branches/PRs and rollback. Required before a canary rollout
  or an authorized downgrade.

Every apply that crosses a boundary carries an exact `approval:*` reference; a
rollback apply approval must equal its authorization approval reference exactly.

## Consumer pin advancement

A consumer (this control plane's module gitlinks, and any runtime-dependency
edge) advances its pin **only to a promoted identity**, never to a raw
default-branch commit chosen for convenience. This rule exists because the
estate today carries three independent pin sources that have already drifted:

- GDS submodule gitlinks (`macos-ubuntu-bootstrap`, `ci-workflows`); the exact
  current pins are recorded in
  `docs/version-ledger.md`, which `scripts/validate_version_ledger.py` checks
  against the tree, and are not restated here;
- the OS bootstrap contract's runtime-clone pins (`codex`, `zcode`) — currently
  behind;
- the harness registry expected heads (`config/repositories.json`) — currently
  ahead.

Until a single canonical estate graph equality-checks these sources (tracked as
the estate-graph canonicalization work), pin advances must be reconciled by
hand against a promoted identity, and a stale consumer pin is a governance
defect, not a cosmetic lag. No pin advance is valid while its target's
promotion evidence is `pending` or `NOT_PROVEN`.

## Rollback

Rollback is the only sanctioned exception to the monotonic sequence floor and
follows the authorized-rollback section of `release-lifecycle.md`: name the
exact lower target sequence and artifact digest, the `InstallScopeDigest`, a
bounded reason, an exact approval reference, and a short expiry. After
verification, issue a corrective release at a new higher sequence; never lower
the durable acceptance floor and never rewrite an existing release.

## Stop conditions

Stop without promotion on any of: a build that is not byte-identical on rebuild;
a missing or non-attested SBOM/provenance; a `NOT_PROVEN` runtime record for a
selected harness whose profile declares `runtime_tests.required: true`; a
consumer pin pointed at an unpromoted identity; a channel skip; a rollback
lacking an exact matching approval reference; or any publication attempt while
the hosted-workflow preconditions in `release-lifecycle.md` are unmet.
