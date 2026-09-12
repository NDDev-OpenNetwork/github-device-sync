# GDS clean-device seed runbook

This procedure brings a supported device with no GDS installation to a verified
release and the estate/harness registration boundary. It describes a procedure,
not a claim that every OS/architecture has been accepted. Record actual device
acceptance and rollback evidence in the consuming estate.

The current boundaries are the public GDS engine, the consuming private estate,
and the pinned OS-bootstrap module. A release does not contain private estate
intent or user credentials. The OS bootstrap does not install GDS.

## Select immutable inputs

The owner supplies the estate repository and exact commit, device descriptor,
release version/sequence/channel, independent consumer trust policy, canonical
installation/state paths and a trusted seed verifier. Use the device's declared
profile: Ubuntu GUI build work uses desktop-builds; headless server and macOS
desktop profiles have different Docker and execution contracts.

Acquire the estate at its selected commit, verify its Git identity and then
initialize its exact submodules. The engine is at modules/github-device-sync
and the OS installer at modules/macos-ubuntu-bootstrap in the consuming layout.
Verify their gitlinks before executing their scripts. Do not clone a second
standalone copy of a consumed module or choose mutable latest/default-branch
contents as installation authority.

GitHub authentication belongs to the device owner and vendor login flow. Do
not copy another device's credentials into the repository, read them into logs,
or place tokens in clone URLs. Local runtime configuration references that
authentication without embedding the secret. Preserve an existing desktop's
session and tools; an OS bootstrap plan is not authority to overwrite them.

## Establish the seed verifier

The first verifier must be trusted independently of the release being checked.
Use a previously trusted binary with an independently authenticated digest, or
an owner-operated reproducible build from a verified source commit and the
registered Go toolchain. Verify the exact seed bytes on the new device before
executing them. Its version must meet the target manifest's minimum CLI version;
a development prerelease can sort below a stable minimum even when its numeric
version looks equal.

Source building is the development/canary seam documented in
[bootstrap-device.md](bootstrap-device.md). With a pinned consuming estate:

```bash
modules/github-device-sync/scripts/bootstrap-device.sh \
  --estate-root . --device estate/devices/<device>.yaml --phase 0 --plan
modules/github-device-sync/scripts/bootstrap-device.sh \
  --estate-root . --device estate/devices/<device>.yaml --phase 1 --apply
```

Source version/builds use the engine root; device intent and registration use
the estate root. A source seed does not by itself become a signed stable
release or authorize installation of an unverified target artifact.

## Acquire and verify the release

Download the exact selected release's six-file release directory and offline
evidence using its immutable identity. Split evidence into the directory shape
specified by [release-lifecycle.md](release-lifecycle.md); failure envelopes and
auxiliary result JSON are not installable release contents. A release page with
only failure evidence is not a usable release.

Obtain the consumer trust policy and trusted-root digest through an independent
approved channel. A policy downloaded alongside the artifact cannot establish
its own trust. With absolute paths to those prepared inputs:

```bash
"$SEED_GDS" --json release verify \
  --release-directory "$RELEASE_DIRECTORY" \
  --evidence-directory "$EVIDENCE_DIRECTORY" \
  --trust-policy "$LOCAL_TRUST_POLICY" \
  --state-path "$GDS_STATE_PATH"
```

Proceed only on success. Bad digests, missing attestations, unsupported minimum
versions and unproven trust stop installation. Preserve the previous accepted
release and monotonic acceptance floor; a failed release/tag is superseded by
a new immutable identity rather than overwritten.

## Install and register

Use the exact release install/upgrade plan, approval, one-shot enablement, apply
and verify commands in [release-lifecycle.md](release-lifecycle.md). Then bind
the device-local estate registration to its device ID, estate repository ID,
canonical root and anchor digest. Derive the private gh-CLI runtime configuration
from the declared installations and prove each account through read-only
inventory. Install only the selected harness adapters through their individual
plans; `gds harness sync` first classifies drift and does not itself apply it.

The phased script prints phase-3 plans and diagnostics. It intentionally rejects
combined phase-3 apply; no single approval silently covers release installation,
estate registration and every harness. The owning task can authorize these
operations, but each journal still binds its own exact identity.

## Acceptance and recovery

Verify the installed executable/version/digest, estate context, doctor, selected
harness discovery, workspace placement and read-only provider reconciliation on
the actual device. Repeat verification and planning to establish idempotence,
and check a fresh login can resolve the installed commands. Record unsupported
or untested surfaces explicitly.

Keep interrupted-download, wrong-digest, unavailable-source, exact installation,
upgrade/rollback and restart/login behavior as separate evidence. Tests on one
prepared desktop do not establish clean-system acceptance for the complete OS
matrix. Resume through the operation journal after failure; do not edit accepted
release state or erase history to make a retry pass.
