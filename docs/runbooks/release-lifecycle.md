# GDS release lifecycle runbook

## Scope

Use this runbook for local verification, installation, upgrade, authorized
rollback, and removal of an immutable GDS release. It does not authorize a
GitHub workflow run, artifact publication, tag, GitHub Release, repository
rollout, merge, or deletion.

## Required inputs

- exact six-file release directory;
- offline evidence directory containing `provenance.sigstore.json`,
  `sbom.sigstore.json`, and `trusted-root.jsonl`;
- independent local consumer trust policy;
- initialized local GDS state database;
- canonical device ID and bounded session ID;
- canonical install root;
- resolved regular `gh` executable with attestation support.

Never obtain the trust policy from the same untrusted release location being
verified. Confirm its expected trusted-root digest through the approved
out-of-band source.

## Hosted workflow preconditions

Do not dispatch `.github/workflows/release-bundle.yml` until both conditions are
proven:

- `scripts/validate_release.sh` passes;
- `stable` and `frozen` receive an isolated signed evidence archive for exactly
  `antigravity-cli`, `claude-code`, `codex`, `cursor-cli`, `grok-build`,
  `opencode`, and `pi`, plus its public
  trust policy;
- repository variable `HARNESS_EVIDENCE_TRUST_POLICY_DIGEST` pins the exact
  `sha256:` digest of that independently distributed public trust policy;
- the repository visibility and active GitHub plan support artifact
  attestations.

The dispatch must provide an explicit `release_sequence` greater than every
sequence already accepted in the consumer ledger. Repository transfer or
republication never resets that ledger, and `github.run_number` is local to one
workflow lineage, so it is not a release sequence.

The whole release chain runs on GitHub-hosted runners. The consumer
does not constrain the runner environment: `gh attestation verify` binds the
attestation to this repository, this reusable-workflow path, this source commit
and this ref, checked against the estate's own trusted root. An owner-controlled
fleet is exactly as authoritative for that identity as an owner-controlled
repository secret, and the previous `--deny-self-hosted-runners` restriction
rejected the estate's own valid releases while adding nothing to it.

Do not split the chain across providers. Provenance describes the environment of
the run that produced it, so building on one provider and attesting on another
yields a claim about an environment the build did not happen in — worse than
either provider used consistently.

The repository is private (ADR 0033) and owned by
the example-org organization, so `actions/attest` provenance and SBOM
attestation is an available release path — keyless Sigstore attestation works
for private repositories as long as the workflow holds `id-token: write`.
Canary may omit active-seven evidence only as explicitly provisional and cannot
auto-promote. Stable/frozen verify the aggregate signature, every isolated
record, exact executable/module/root versions, GDS profile and bridge digests,
freshness (maximum 72 hours), and the complete active set. Only signed artifacts
and public trust material enter workflow inputs; private signing keys never do.

The producer workflow is defined in the supporting `example-harnesses`
repository at `docs/gds-runtime-evidence.md`. Its deterministic flat archive is
the value encoded for `harness_evidence_archive_base64`; the independently
distributed public policy is encoded for
`harness_evidence_trust_policy_base64`. The policy identity needs both
`harness-evidence` and `harness-evidence-aggregate` roles. Before dispatch,
decode both into a temporary directory and run a local stable
`gds-release-builder` build against them; producer self-consistency is not a
substitute for compatibility with the real GDS verifier.

The gate was exercised end to end historically: run `30046936069` built the
canary-channel bundle from `refs/heads/main` (2026-07-23), and run
`30064955206` built, attested, and published the stable `gds-v0.1.0` bundle
from `refs/tags/gds-v0.1.0` (2026-07-24T10:11:01Z) with keyless Sigstore SLSA
build provenance and an SBOM attestation. Releases through `gds-v0.3.6` were
signed on self-hosted runners; they were previously unusable as bootstrap
candidates because the consumer rejected any self-hosted signer, and that
restriction is gone, so they are ordinary releases judged on their own contents.
A prior successful dispatch is still not an approval: every bootstrap release
requires its own exact `A5` approval.

## Read-only verification

```bash
gds --json release verify \
  --release-directory "$RELEASE_DIRECTORY" \
  --evidence-directory "$EVIDENCE_DIRECTORY" \
  --trust-policy "$LOCAL_TRUST_POLICY" \
  --state-path "$GDS_STATE_PATH"
```

Continue only when the result is `success`. `NOT_PROVEN` is a stop condition,
not a warning to bypass.

## Install or upgrade

Plan:

```bash
gds --json --cwd "$GDS_CONTROL_PLANE" release install --plan \
  --release-directory "$RELEASE_DIRECTORY" \
  --evidence-directory "$EVIDENCE_DIRECTORY" \
  --trust-policy "$LOCAL_TRUST_POLICY" \
  --install-root "$GDS_INSTALL_ROOT" \
  --state-path "$GDS_STATE_PATH" \
  --device-id "$GDS_DEVICE_ID" \
  --session-id "$GDS_SESSION_ID"
```

Use `upgrade` instead of `install` when one accepted release is active. Review
the exact candidate, current target, file set, sequence, digests, source
identity, and plan expiry. Apply the stored plan with the same exact evidence
paths and the signed exact-plan approval JSON file:

```bash
gds --json --cwd "$GDS_CONTROL_PLANE" release install \
  --apply "$PLAN_ID" \
  --release-directory "$RELEASE_DIRECTORY" \
  --evidence-directory "$EVIDENCE_DIRECTORY" \
  --trust-policy "$LOCAL_TRUST_POLICY" \
  --install-root "$GDS_INSTALL_ROOT" \
  --state-path "$GDS_STATE_PATH" \
  --device-id "$GDS_DEVICE_ID" \
  --session-id "$GDS_SESSION_ID" \
  --approval-ref "$SIGNED_APPROVAL_JSON"
```

Verify using only stored identity and state:

```bash
gds --json --cwd "$GDS_CONTROL_PLANE" release install \
  --verify "$OPERATION_ID" \
  --state-path "$GDS_STATE_PATH" \
  --device-id "$GDS_DEVICE_ID" \
  --session-id "$GDS_SESSION_ID"
```

Do not pass release, evidence, trust, or install paths to post-apply
verification. They are intentionally rejected.

## Authorized rollback

Rollback is an explicit exception to the monotonic sequence floor. Prepare a
schema-valid authorization with:

- rollout ID;
- lower installed target sequence and artifact digest;
- `InstallScopeDigest(canonical-install-root, trust-domain)`;
- bounded reason;
- exact `approval:*` reference;
- expiry after apply and short enough for the incident.

Resolve the exact canonical scope and active release first:

```bash
gds --json release scope \
  --install-root "$GDS_INSTALL_ROOT" \
  --trust-policy "$LOCAL_TRUST_POLICY"
```

Copy the returned `scope_digest`, target sequence, and target artifact digest
into the reviewed authorization; do not reproduce the canonical JSON hashing
algorithm manually.

Plan and apply:

```bash
gds --json --cwd "$GDS_CONTROL_PLANE" release rollback --plan \
  --install-root "$GDS_INSTALL_ROOT" \
  --target-release-key "$TARGET_RELEASE_KEY" \
  --rollback-authorization "$ROLLBACK_AUTHORIZATION" \
  --state-path "$GDS_STATE_PATH" \
  --device-id "$GDS_DEVICE_ID" \
  --session-id "$GDS_SESSION_ID"

gds --json --cwd "$GDS_CONTROL_PLANE" release rollback \
  --apply "$PLAN_ID" \
  --install-root "$GDS_INSTALL_ROOT" \
  --target-release-key "$TARGET_RELEASE_KEY" \
  --rollback-authorization "$ROLLBACK_AUTHORIZATION" \
  --state-path "$GDS_STATE_PATH" \
  --device-id "$GDS_DEVICE_ID" \
  --session-id "$GDS_SESSION_ID" \
  --approval-ref "$ROLLBACK_APPROVAL_REF"
```

The apply approval must exactly equal the authorization approval reference.
After verification, create a corrective release with a new higher sequence;
never lower the durable acceptance floor or rewrite an existing release.

## Remove active release

Removal deletes only the exact active accepted release. It leaves inactive
release directories intact for explicit later retention/recovery decisions.

```bash
gds --json --cwd "$GDS_CONTROL_PLANE" release remove --plan \
  --install-root "$GDS_INSTALL_ROOT" \
  --state-path "$GDS_STATE_PATH" \
  --device-id "$GDS_DEVICE_ID" \
  --session-id "$GDS_SESSION_ID"
```

Apply and verify with the common lifecycle flags. An active rollback release
below the highest sequence can be removed only when its exact sequence/digest
binding already exists in the acceptance ledger.

## Stop conditions

Stop without mutation for any:

- missing or substituted release/evidence/trust file;
- unpinned or changed trusted root;
- source owner, repository, workflow, ref, or commit mismatch;
- missing target binaries or SBOM coverage;
- stale repository, manifest, policy, active release, or candidate digest;
- sequence conflict or unauthorized downgrade;
- uncommitted trust-critical GDS implementation source;
- missing approval, lock conflict, kill switch, or failed verification.

Use `gds operation inspect <operation-id>` and the recovery workflow for a
partial operation. Do not repair `current`, installed records, or the
acceptance database manually.
