# GDS clean-device seed runbook

## Scope

Use this runbook for the zero-to-one seam on a **stock supported device that has
no `gds` binary, no cloned control plane, and no initialized GDS state**: how an
owner brings that device from bare OS to a point where the
`gds-bootstrap-device` skill can take over. It is the documented entrypoint that
currently precedes `gds release verify`; nothing earlier existed before this
file.

This runbook does not authorize a hosted release, artifact publication, tag,
GitHub Release, repository rollout, or any external mutation. It does not
weaken the release trust policy or the `release-lifecycle.md` stop conditions.

Supported seed targets match the OS bootstrap contract: macOS `arm64`, and
Ubuntu `24.04`/`26.04` (`amd64`/`arm64`).

## Status

- macOS `arm64`: seed steps 0–4 are locally rehearsable; step 2b can use the
  published release, and the seed verifier of step 2a is owner-operated. A full
  stock-device rehearsal of the whole graph is **`NOT_PROVEN`**.
- Ubuntu `24.04`/`26.04`: **`NOT_PROVEN`.** Linux consumer execution remains
  `NOT_PROVEN` (completion plan residual #8; stage `C9`). Do not claim a
  clean-Ubuntu acceptance that was not produced on a real VM.
- The first external immutable release exists: `gds-v0.1.0` (source commit
  `bace996`) was published on 2026-07-24T10:11:01Z with the six-file release
  directory attached and keyless Sigstore SLSA provenance plus an SBOM
  attestation. The hosted-workflow preconditions in `release-lifecycle.md` are
  met (private example-org repository; harness runtime proof is delegated out of
  the release gate). Its published assets are the six release files only: the
  offline evidence attachment landed in the workflow after that tag, so for
  `gds-v0.1.0` the evidence directory must still be reconstructed out-of-band.
  Publication does not accept the bundle on this device — step 3 verification
  still governs.

Authority: `docs/migration/gds-completion-plan.md`. Typed handoff contract:
`docs/contracts/seed-bootstrap-v1.md`. Higher-level sequencing:
`docs/runbooks/bootstrap-device.md` (the `scripts/bootstrap-device.sh` orchestrator
that acquires the Go toolchain and drives this seam on a canary/source-build
device).

## Prerequisites (non-secret)

- Device identity and profile (device ID, OS/architecture, selected harnesses).
- Approved out-of-band value of the expected trusted-root digest, and of the
  seed-verifier digest (step 2a). Never take the trust policy or either digest
  from the same location that serves the release being verified.
- Canonical install root and a path for the local GDS state database.
- Owner-controlled GitHub authentication handled through the bootstrap auth
  handoff, which is non-secret and never reads, prints, stores, or uploads
  credentials (`modules/macos-ubuntu-bootstrap/scripts/auth-handoff.sh`).

## Step 0 — Acquire the bootstrap implementation

Every later command uses an absolute path defined by an earlier step. On a stock
device nothing in this repository is present yet, so the first act is to obtain
the pinned OS bootstrap implementation and prove which commit it is.

The selected implementation is `example-org/macos-ubuntu-bootstrap` release
`2.6.1`, commit `6c4e953a0c3699103f3bcac233f9b0c87eea00ec`. The seed deliberately
follows the immutable release tag rather than the control plane's current gitlink:
the gitlink may sit ahead of the tag on documentation-only commits, and a
zero-to-one path must depend on something that cannot move. `docs/version-ledger.md`
records the current gitlink and labels any skew. Do not clone a mutable default
branch and do not pipe a remote script into a shell.

```bash
BOOTSTRAP_COMMIT=6c4e953a0c3699103f3bcac233f9b0c87eea00ec
BOOTSTRAP_ROOT="$HOME/.local/share/gds-seed/macos-ubuntu-bootstrap"

mkdir -p -- "$(dirname -- "$BOOTSTRAP_ROOT")"
git clone --no-checkout \
  https://github.com/example-org/macos-ubuntu-bootstrap.git "$BOOTSTRAP_ROOT"
git -C "$BOOTSTRAP_ROOT" fetch --depth 1 origin "$BOOTSTRAP_COMMIT"
git -C "$BOOTSTRAP_ROOT" checkout --detach "$BOOTSTRAP_COMMIT"
```

`git` is present on a stock Mac through the Command Line Tools; if it is not,
macOS prompts to install them on first invocation. Verify the selected identity
before running anything from the checkout, and stop if either check disagrees:

```bash
test "$(git -C "$BOOTSTRAP_ROOT" rev-parse HEAD)" = "$BOOTSTRAP_COMMIT"
test "$(cat -- "$BOOTSTRAP_ROOT/VERSION")" = "2.0.0"
```

An owner who already has a verified control-plane checkout on this device may
instead set `BOOTSTRAP_ROOT` to its `modules/macos-ubuntu-bootstrap` path, after
confirming the same two checks. Every following step uses `$BOOTSTRAP_ROOT`.

## Step 1 — OS bootstrap (dev tools, not GDS)

Compose the base runtime with the pinned OS bootstrap adapter. This installs
Homebrew/LSPs, the immutable Node/uv/Bun runtime, the AI CLIs through their
owner modules, and the required CloakBrowser service. It does **not** install
`gds` and does not touch the control-plane release flow.

```bash
# plan first
bash "$BOOTSTRAP_ROOT/scripts/bootstrap.sh" --platform macos
bash "$BOOTSTRAP_ROOT/scripts/bootstrap.sh" --platform ubuntu --profile desktop
# apply only after reviewing the plan, on the target device
bash "$BOOTSTRAP_ROOT/scripts/bootstrap.sh" --platform macos --apply
```

At release 2.0.0 the bootstrap installs the selected harnesses itself: when
`RLDYOUR_CODEX_MODULE` / `RLDYOUR_ZCODE_MODULE` are unset it clones each owner
module at its exact contract commit and runs that module's install lifecycle.
Set those variables only to override with an existing local checkout. Bootstrap
does **not** install `gds` and does **not** install the seed verifier of step 2.

The OS bootstrap and the GDS control plane are two mutation boundaries. This
step ends with dev tools present but no `gds` on the device.

## Step 2 — Establish a seed verifier and obtain the release

Step 3 runs `gds release verify`. That `gds` cannot come from the release it is
about to verify — the trust chain would be circular — and the initial state of
this runbook says no `gds` is installed. So the seed verifier is acquired first,
under its own trust mechanism, and is retired once a verified release binary is
installed.

### 2a — Seed verifier (`$SEED_GDS`)

Use exactly one of the two mechanisms, in trust order. Both end with an absolute
`SEED_GDS` path and a recorded digest.

1. **Owner-operated reproducible build on an already-trusted machine.** From a
   fully tracked clean worktree of this control plane, at the commit whose
   release is being installed:

   The version must be injected, not left at its development default. A plain
   `go build` leaves `cli.Version` at `0.1.0-dev`, and the release verifier
   compares that value to the manifest's `minimum_cli_version` with SemVer
   precedence — where a prerelease sorts *below* the release of the same
   number, so `0.1.0-dev` fails a `0.1.0` floor with
   `GDS_RELEASE_CLI_VERSION_BLOCKED`. Build the seed with the same identity the
   release builder injects, at or above the floor of the release being
   installed:

   ```bash
   SEED_VERSION=0.1.0        # >= the target release manifest's minimum_cli_version
   GOTOOLCHAIN=go1.26.7 go build -trimpath \
     -ldflags "-X github.com/NDDev-OpenNetwork/github-device-sync/core/cli.Version=$SEED_VERSION" \
     -o gds ./core/cmd/gds
   shasum -a 256 gds          # record; this is the seed digest
   ```

   The seed's trust comes from this out-of-band build and its recorded digest,
   not from matching the released binary byte for byte: the release builder adds
   `-s -w -buildid=` and the two artifacts are deliberately not the same bytes.

   Transfer the binary to the seed device out-of-band, place it at an absolute
   path, and re-check the digest there:

   ```bash
   SEED_GDS="$HOME/.local/share/gds-seed/bin/gds"
   shasum -a 256 -- "$SEED_GDS"   # must equal the recorded digest
   chmod 0755 -- "$SEED_GDS"
   ```

   The reproducible builder requirement (byte-identical rebuild) is the same one
   the release gate enforces (`docs/contracts/bundle-release-v1.md`), so an
   independent rebuild of the same commit reproduces this digest.

2. **Previously trusted transfer.** A `gds` binary carried from a device where
   it was already verified, authenticated on arrival against a digest delivered
   through the same approved out-of-band channel as the trusted-root digest —
   never through the location serving the release.

Both mechanisms must satisfy the compatibility floor before use. `--version` is
the supported query; there is no `version` subcommand:

```bash
"$SEED_GDS" --version         # prints: gds version <semver>
```

Read the SemVer it prints and stop unless it is greater than or equal to the
`minimum_cli_version` in the target release's `manifest.json` (step 2b downloads
that manifest; the floor can be read before verification because step 3 checks
it again against the signed manifest).

Never obtain the seed by extracting it from the unverified target release, by a
mutable `latest` download, or by piping a remote installer into a shell.

### 2b — Release and evidence assets

The release workflow attaches the six release-directory files **and** the
offline evidence files to one GitHub Release, while the consumer requires two
separate directories and rejects a release directory that does not hold exactly
six entries. Download into a staging directory and split explicitly:

```bash
RELEASE_TAG=gds-v0.1.0
STAGING="$(mktemp -d)"
RELEASE_DIRECTORY="$HOME/.local/share/gds-seed/release"
EVIDENCE_DIRECTORY="$HOME/.local/share/gds-seed/evidence"

gh release download "$RELEASE_TAG" \
  -R example-org/github-device-sync --dir "$STAGING"

mkdir -p -- "$RELEASE_DIRECTORY" "$EVIDENCE_DIRECTORY"
for name in \
  manifest.json release-envelope.json bundle-trust.yaml SHA256SUMS \
  sbom.spdx.json "gds-bundle-${RELEASE_TAG#gds-}.tar.gz"
do
  cp -- "$STAGING/$name" "$RELEASE_DIRECTORY/$name"
done
for name in provenance.sigstore.json sbom.sigstore.json trusted-root.jsonl; do
  cp -- "$STAGING/$name" "$EVIDENCE_DIRECTORY/$name"
done
```

Auxiliary result JSON published alongside the evidence (`build-result.json`,
`verification-result.json`, `trusted-root-verification.json`) is diagnostic
only. Leave it in `$STAGING`; copying it into either consumer input breaks the
contract. Preflight before step 3, and stop on any mismatch:

```bash
test "$(find "$RELEASE_DIRECTORY" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')" = 6
test ! "$(find "$RELEASE_DIRECTORY" "$EVIDENCE_DIRECTORY" -mindepth 1 ! -type f)"
for name in provenance.sigstore.json sbom.sigstore.json trusted-root.jsonl; do
  test -s "$EVIDENCE_DIRECTORY/$name"
done
```

Releases published before the evidence assets were attached carry only the six
release files. **`gds-v0.1.0`, the only tag published so far, is one of them**:
its assets are `manifest.json`, `release-envelope.json`, `bundle-trust.yaml`,
`SHA256SUMS`, `sbom.spdx.json`, and `gds-bundle-v0.1.0.tar.gz`, and nothing else.
The evidence copy loop above therefore has nothing to copy for that tag and the
preflight stops the seed. That is the intended outcome, not a silent skip — but
it also means the flow is not closed by `gh release download` alone.

Retrieve the evidence from the immutable artifact of the workflow run that
produced the tag:

```bash
RELEASE_RUN=$(gh run list -R example-org/github-device-sync \
  --workflow release-bundle.yml --json databaseId,headSha,conclusion \
  --jq 'map(select(.conclusion=="success"))[0].databaseId')
gh run download "$RELEASE_RUN" -R example-org/github-device-sync --dir "$STAGING/run"
find "$STAGING/run" -type f \
  \( -name provenance.sigstore.json -o -name sbom.sigstore.json \
     -o -name trusted-root.jsonl \) -exec cp -- {} "$EVIDENCE_DIRECTORY/" \;
```

If the run's retention window has expired the evidence is unrecoverable for that
tag, and the correct action is to cut a new release that attaches it rather than
to seed a device without verification.

### 2c — Materialize the trust inputs

Step 3 needs two absolute paths that no earlier download provides. Establish
them here, because the trust policy must not come from the location that served
the release:

```bash
LOCAL_TRUST_POLICY="$HOME/.local/share/gds-seed/consumer-trust.yaml"
GDS_STATE_PATH="$HOME/.local/state/gds/seed.db"
mkdir -p -- "$(dirname -- "$LOCAL_TRUST_POLICY")" "$(dirname -- "$GDS_STATE_PATH")"
```

Place the consumer trust policy at `$LOCAL_TRUST_POLICY` from the same approved
out-of-band channel that delivered the seed digest — the owner's trusted machine
or an internal distribution path — and confirm its digest there. The
`bundle-trust.yaml` inside `$RELEASE_DIRECTORY` is the release's own claim about
itself and is not a substitute. `$GDS_STATE_PATH` is created by the first
command that writes it; only its parent directory must exist.

Never take the trust policy or the trusted root from the same location that
served the release.

## Step 3 — Verify before install

Run the read-only verification exactly as in `release-lifecycle.md`. `NOT_PROVEN`
is a stop condition, not a warning:

```bash
"$SEED_GDS" --json release verify \
  --release-directory "$RELEASE_DIRECTORY" \
  --evidence-directory "$EVIDENCE_DIRECTORY" \
  --trust-policy "$LOCAL_TRUST_POLICY" \
  --state-path "$GDS_STATE_PATH"
```

Continue only on `success`.

After step 4 installs the verified release, prove the installed binary's
identity (`gds --version` plus its recorded digest) and only then quarantine or
delete `$SEED_GDS`. Do not keep an unverified seed on the device as a fallback
verifier.

## Step 4 — Hand off to the device bootstrap skill

Authority now passes to `skills/canonical/gds-bootstrap-device/SKILL.md`, which
runs the staged, plan-first install with approval at each apply: pinned bundle
install (`release install --plan/--apply/--verify`), estate registration,
selected harness render/install, and `gds doctor`. Follow that skill; this
runbook does not duplicate its verbs.

## Resumability and recovery

- Every device mutation is plan → apply `<plan-id>` `--approval-ref` → verify,
  and is idempotent on rerun. A partial operation is inspected with
  `gds operation inspect <operation-id>`; do not repair installed records or the
  acceptance database by hand.
- An interrupted seed re-enters at the first step whose result is not durably
  recorded. Reboot/login between steps is safe.
- Rollback of an installed release follows the authorized rollback section of
  `release-lifecycle.md`; it is an explicit exception to the monotonic sequence
  floor and needs a schema-valid authorization plus an exact approval reference.

## Stop conditions

Stop without mutation on any of: a bootstrap checkout whose HEAD or `VERSION`
disagrees with step 0; a seed verifier whose digest was not independently
authenticated, or that was taken from the release it verifies; a release
directory that does not hold exactly six regular files, or an evidence directory
missing one of the three required inputs; an unpinned or changed trusted root; a release
obtained from an untrusted location whose trust policy came from that same
location; a missing approval reference; a `NOT_PROVEN` verification result; or
an attempt to install on Ubuntu and claim acceptance without real VM evidence.
