# GDS migration rollback runbook

Status: required before target runtime activation.

## Principles

- Rollback restores a previous immutable state; it does not rewrite pushed
  history.
- A failed phase must remain inspectable.
- Legacy behavior is removed only after parity and recovery tests.
- External mutation credentials are separable from read-only credentials.
- Rollback never means a generic force-push or allow-older-versions switch.

## Current baseline

- Control-plane repository: example-org/github-device-sync (private)
- Restorable baseline: release tag `gds-v0.1.0`, published 2026-07-24 with an
  SPDX SBOM, `SHA256SUMS`, a release envelope, and Sigstore build-provenance and
  SBOM attestations over the bundle artifact.
- Historical migration baseline HEAD:
  433c46b6923f7dc1efb96713b9ffc9330ca8ba58
- Design snapshot digest:
  66d2cfb377be48b9912c5a5ee4454a808fa3f8770808ca899ae560fa325d81d4
- Inventory evidence: `gds inventory relationships` (the former
  `artifacts/inventory/` directory was removed when the repository was published)

`gds-v0.1.0` is the first immutable artifact this runbook can restore to. Verify
restoration in an isolated fixture before any runtime replacement or external
mutation; do not treat a dirty worktree as a restorable release.

## Kill switches

Target runtime must expose and report:

- GDS_MUTATIONS_DISABLED=true
- GDS_WEBHOOK_PROCESSING_READ_ONLY=true
- GDS_ROLLOUT_PAUSED=true
- GDS_HARNESS_HOOKS_DISABLED=true

Every operation result must include effective kill-switch state.

## Rollback by phase

### Documentation and schemas

- stop using the candidate schema version;
- retain files and failure evidence;
- restore the previous accepted schema/bundle reference through a new commit;
- rerun migration and invalid-fixture tests.

### Read-only CLI

- disable the candidate entrypoint;
- continue using legacy read-only status/doctor only;
- preserve fixture outputs and JSON contract failures;
- do not fall back to legacy mutators automatically.

### Projections and harness adapters

- disable candidate hooks;
- restore the previous verified bundle digest;
- regenerate projections from that immutable bundle;
- verify instruction and skill discovery in clean harness fixtures.

### Local mutations

- stop the operation journal at the failed step;
- reacquire no lock until state is re-observed;
- run an explicit recovery plan;
- use compensating commits or restored pins rather than history rewrite.

### GitHub controller

- revoke or disable mutation credentials;
- keep inventory in read-only mode;
- pause webhooks or process them as observation-only;
- preserve delivery IDs, plan IDs, request IDs, and redacted evidence;
- reconcile current provider state before any retry.

### Rollout

- stop new waves;
- name the exact previous bundle version, release sequence, digest, and
  attestation identity;
- authorize only that exact downgrade for the rollout ID;
- open corrective repository changes instead of rewriting merged history;
- restore the anti-rollback floor after verification.

## Recovery evidence

Record:

- incident and operation IDs;
- failed phase and step;
- before/after object IDs;
- affected repositories;
- exact bundle and schema versions;
- checks run and checks not proven;
- compensation applied;
- final reconciliation result.

## Forbidden rollback shortcuts

- force-pushing main;
- deleting failure branches or logs before review;
- changing a mutable tag to point at older content;
- bypassing failed attestation with a checksum-only install;
- broad rm -rf cleanup;
- silently ignoring partial completion;
- marking unavailable verification as passed.
