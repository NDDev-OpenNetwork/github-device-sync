# GDS phased device bootstrap runbook

## Scope

This runbook documents the single entry point that brings a new device through
the three GDS mutation boundaries in order:

```text
preflight -> source seed -> optional OS bootstrap -> control-plane plans
```

The entry point is `scripts/bootstrap-device.sh`, a phased orchestrator that
reads a device descriptor (`estate/devices/<device>.yaml`) and derives the
`macos-ubuntu-bootstrap` OS-installer flags from the descriptor's optional
`class:` block, so the device intent and the OS installer it drives cannot
disagree.

The public engine and private consumer are separate Git roots. When the engine
is consumed as a gitlink, run it from the estate with an explicit root:

```bash
modules/github-device-sync/scripts/bootstrap-device.sh \
  --estate-root . --device estate/devices/<device>.yaml --phase 0 --plan
```

The script proves that its engine checkout and the sibling OS bootstrap checkout
match declared estate gitlinks and have no uncommitted changes. Device paths,
registration and runtime configuration resolve against the selected estate;
source version and Go builds resolve against the engine. The default source-root
mode remains available for standalone development layouts. Do not create a
second standalone copy of an already-consumed module.

It is the wrapper over the canonical, lower-level runbooks:

- `seed-clean-device.md` — the zero-to-one seam for a stock device with no
  `gds` and no state. This runbook is the higher-level sequencing that calls it.
- `release-lifecycle.md` — the per-device `release install/upgrade/rollback/remove`
  contract used by phase 3a.

It does **not** add a `gds bootstrap` CLI verb. The control plane keeps
plan/approval/apply/verify at each boundary; this orchestrator only sequences
them. Nothing clones a mutable default branch and nothing edits `~/.bashrc`
silently.

## Device class

A device descriptor may declare an optional `class:` block whose vocabulary is
mirrored from `modules/macos-ubuntu-bootstrap/config/rldyour-contract.json`:

| Field | Values | Notes |
|---|---|---|
| `profile` | `desktop` \| `desktop-builds` \| `server` | LSP-only workstation vs build workstation with Docker vs headless container host |
| `gui` | `enabled` \| `disabled` | server is always `disabled` |
| `docker_mode` | `none` \| `rootful` \| `rootless` | desktop requires `none`; desktop-builds requires `rootful`; macOS never installs Docker |
| `execution_policy` | `source-lsp-only` \| `local-dev-with-builds` \| `container-execution-only` | must match profile |
| `hardening.{ssh,ufw,fail2ban}` | `true` \| `false` | server only |

Cross-field rules are enforced by the schema validator
(`GDS_DEVICE_CLASS_*` findings), mirroring the rules in
`modules/macos-ubuntu-bootstrap/scripts/bootstrap.sh`. A descriptor that omits
`class:` stays valid and defaults to desktop.

The bootstrap contract also defines the execution seam between classes. A
`desktop` may run an explicit command on a separately provisioned `server`
through `modules/macos-ubuntu-bootstrap/scripts/remote-exec.sh`; both
repositories must be clean and resolve to the same exact commit. No source or
credential transfer and no implicit remote checkout repair occur. A
`desktop-builds` device instead performs its builds locally.

## Phases

### Phase 0 — preflight (read-only)

Verifies the host OS/arch, the control-plane root, the bootstrap submodule, and
`gh` authentication. Reports the derived OS-installer flags. Mutates nothing.

### Phase 1 — seed Go + build gds

Installs the pinned Go toolchain (`go1.27.1`, the security floor) into
`~/sdk/go<version>` (the `GOTOOLCHAIN` pattern) and builds the `gds` CLI from
the control-plane source into `~/.local/bin/gds`. The source build carries the
nearest release version plus the exact source commit (for example,
`0.3.6+source.0123456789ab`). Phase 1 compares that identity before deciding
the installed binary is current; a changed commit is rebuilt, while a dirty
Go source tree is rebuilt on every apply. This makes the phase idempotent
without accepting an older runnable binary as current. The
`macos-ubuntu-bootstrap` submodule deliberately does **not** install Go or
`gds` (they belong to the control-plane boundary), so this phase acquires them.

On a production device that consumes a published release, prefer the
`seed-clean-device.md` path instead: acquire the release out-of-band, verify
it, and use the verified binary. Source-build is the development/canary path.

### Phase 2 — OS bootstrap

Privileged operations use the OS installer's existing sudo/PolicyKit route.
If it requests an interactive password, the owner enters it directly; never
collect or pipe that password. Existing authorized passwordless sudo does not
require a second confirmation. Review the plan before applying an installer to
an already-provisioned desktop so its existing session and managed tools are
preserved.

Invokes
`bash modules/macos-ubuntu-bootstrap/scripts/bootstrap.sh --platform <p> --profile <p> [--gui|--no-gui] [--docker-mode <m>] [--apply|--plan]`
with flags derived from the descriptor's `class:` block. This installs dev
tools, language hosts (Node/uv/Bun), selected harness CLIs, and the browser layer. It never
installs `gds`. Use `--plan` (default) for a dry-run first.

Desktop contents and exact artifact versions belong to the selected
`macos-ubuntu-bootstrap` contract, including its Google Chrome GUI choice.
There is no BrowserOS/CloakBrowser provisioning prerequisite in this GDS path.
Each OS operation remains plan-aware and independently verifiable.

From the selected estate root, apply only the separately reviewed OS phase:

```bash
modules/github-device-sync/scripts/bootstrap-device.sh \
  --estate-root . --device estate/devices/<device>.yaml --phase 2 --apply
```

### Phase 3 — control-plane staged

Each step keeps its own plan/approval/apply/verify. The orchestrator reports
plans and diagnostics; it does not combine their writes or approvals. These
steps do not require sudo.

- **3a release install** (release mode only) — skipped when bootstrapping from
  source, since there is no release directory. In release mode, set
  `RELEASE_DIRECTORY`, `EVIDENCE_DIRECTORY`, `LOCAL_TRUST_POLICY`, and
  `GDS_INSTALL_ROOT` in the environment.
- **3b workspace register-estate** — registers the device-local control-plane
  locator (`repository_id` + `root` + `anchor_digest`). This is the
  authoritative binding of this checkout to the control plane. Re-running the
  phase skips registration only when the device, repository, canonical root,
  and current anchor digest all match; an anchor change is refreshed through a
  new plan/apply/verify transaction.
- **3b' gh CLI runtime config** — derives a private `0600`
  `github-runtime.yaml` (under `$XDG_CONFIG_HOME/github-device-sync/`) from the
  estate installations so the device can observe and reconcile its estate
  through the already-authenticated `gh` CLI (ADR 0034), with no GitHub App
  private key. Each installation binds to its declared account; the secret
  references mirror the estate exactly. One live inventory read per
  installation proves the binding. Skipped (warned) when `gh` is not
  authenticated or its token is unreadable.
- **3c harness sync** — classifies drift read-only. Every reported action is
  executed later as its own exact signed and enabled transaction. For an unselected provisional adapter that cannot
  render, absence of its unique GDS lock marker proves it was never installed;
  a present or unreadable marker remains fail-closed as unobservable.
- **3d gds doctor** — aggregate read-only diagnostic.

## Usage

```bash
# Plan the whole bootstrap (read-only)
modules/github-device-sync/scripts/bootstrap-device.sh --estate-root . \
  --device estate/devices/<device>.yaml --plan

# Apply only installer phases 0-2
modules/github-device-sync/scripts/bootstrap-device.sh --estate-root . \
  --device estate/devices/<device>.yaml --phase 1 --apply

# Phase 3: run each printed plan command, sign its exact digest, then use
# scripts/gds-exact-apply.sh for the separate enable/apply/verify sequence.

# Combined phase-3 apply is intentionally rejected.
```

For an already-approved release lifecycle plan, pass its exact identity inputs
to the helper. It supplies those inputs to apply, then verifies the stored
operation without replaying release paths or rollback selectors:

```bash
scripts/gds-exact-apply.sh --plan-id <plan-id> --approval-file <approval.json> \
  --state-path <db> --device-id <id> --session-id <id> -- \
  gds --json release install --install-root <root> \
  --release-directory <release> --evidence-directory <evidence> \
  --trust-policy <independent-trust-policy>
```

The same rule applies to upgrade, rollback, and remove. Harness lifecycle
verification retains its harness and target selectors. Failure in enable,
apply, or verify stops the sequence and never reports a successful result.

At the end the orchestrator prints the `export PATH` line to add the Go
toolchain. It does **not** edit `~/.bashrc` silently.

## Device integrity receipt

The OS installer owns the device integrity receipt after its verification
passes. Combined phase-3 apply is disabled, so the read-only phase 3d does not
create that receipt. A receipt is a canonical-JSON snapshot binding the device
to the OS contract actually verified. The receipt lives at
`~/.local/share/rldyour/device-receipt.json` (mode `0600`), mirroring the
architecture of the browser runtime receipt.

`modules/macos-ubuntu-bootstrap/scripts/device_integrity.py` provides three
subcommands:

```bash
# Build (or rebuild) the receipt from the current device state
python3 modules/macos-ubuntu-bootstrap/scripts/device_integrity.py build

# Verify the device matches its receipt AND the contract (read-only)
python3 modules/macos-ubuntu-bootstrap/scripts/device_integrity.py verify [--json]

# Validate receipt self-integrity before an atomic replacement
python3 modules/macos-ubuntu-bootstrap/scripts/device_integrity.py metadata-only --receipt <path>
```

`verify` performs two checks: (1) re-collect the device state and compare it
structurally to the stored receipt (a binary changed, a file vanished, a path
moved); (2) compare every declared runtime/tool version against
`rldyour-contract.json` (closing the gap where `verify.sh` compares against
hardcoded literals). Either failing is `status: NOT_PROVEN`.

Phase 0 (preflight) also runs `verify` read-only to report drift *before* any
mutation.

## Status

- The source bootstrap pins reviewed Go archives for Linux and macOS, on
  amd64 and arm64. Go 1.27 requires macOS 13 or later.
- Use `seed-clean-device.md` for release-bound initialization. Availability
  of a platform binary and a passing source build do not certify a new device;
  verify its OS receipt, independent release trust, exact installation, GitHub
  access, selected harnesses, and doctor result on that device.
- Device-specific rehearsal and acceptance records belong to the consuming
  estate. An example descriptor is not a live acceptance result.
- Source-build phase 1 is a development/canary path and is not a release
  artifact. A hosted release remains the production boundary.

## Stop conditions

Stop on untrusted artifact, unsupported platform, config collision, secret
leak, hook trust failure, stale plan, or any unexpected system write — the
same `release-lifecycle.md` stop conditions, plus the OS-installer's own
failures.

## Authority

`docs/contracts/authority-and-change-protocol-v1.md`. Lower-level contracts:
`docs/contracts/seed-bootstrap-v1.md`, `docs/contracts/bundle-release-v1.md`.
