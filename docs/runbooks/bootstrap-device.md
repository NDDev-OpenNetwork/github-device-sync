# GDS phased device bootstrap runbook

## Scope

This runbook documents the single entry point that brings a new device through
the three GDS mutation boundaries in order:

```text
OS bootstrap  ->  seed (Go toolchain + gds)  ->  control-plane staged commands
```

The entry point is `scripts/bootstrap-device.sh`, a phased orchestrator that
reads a device descriptor (`estate/devices/<device>.yaml`) and derives the
`macos-ubuntu-bootstrap` OS-installer flags from the descriptor's optional
`class:` block, so the device intent and the OS installer it drives cannot
disagree.

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

Installs the pinned Go toolchain (`go1.26.5`, the security floor) into
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

**This phase requires interactive sudo.** The agent cannot enter the password.
Present the exact command to the owner and wait for confirmation.

Invokes
`bash modules/macos-ubuntu-bootstrap/scripts/bootstrap.sh --platform <p> --profile <p> [--gui|--no-gui] [--docker-mode <m>] [--apply|--plan]`
with flags derived from the descriptor's `class:` block. This installs dev
tools, language hosts (Node/uv/Bun), selected harness CLIs, and the browser layer. It never
installs `gds`. Use `--plan` (default) for a dry-run first.

On Ubuntu desktop (`profile: desktop`, `gui: enabled`), the OS bootstrap also
calls `scripts/ubuntu/desktop.sh`, which:
- moves the GNOME dock to the bottom (macOS-style);
- adds a Russian keyboard layout with Alt+Shift toggle;
- installs BrowserOS (open-source agentic browser, `.deb`);
- removes the stock snap + apt Firefox completely.

Each desktop step is independent and idempotent; sudo is refreshed per-step.

**Exact command for the owner (Ubuntu desktop example):**
```bash
cd ~/Developer/control-plane/github-device-sync
scripts/bootstrap-device.sh --device estate/devices/example-user-ubuntu-1.yaml --apply --from-phase 2
```
The script prompts for sudo and runs to completion. If a step fails on expired
sudo, re-running resumes from the failed phase.

### Phase 3 — control-plane staged

Each step keeps its own plan/approval/apply/verify. The orchestrator extracts
plan and operation ids from the JSON envelopes and threads them through. These
steps do **not** require sudo.

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
scripts/bootstrap-device.sh --device estate/devices/<device>.yaml --plan

# Apply only installer phases 0-2
scripts/bootstrap-device.sh --device estate/devices/<device>.yaml --phase 1 --apply

# Phase 3: run each printed plan command, sign its exact digest, then use
# scripts/gds-exact-apply.sh for the separate enable/apply/verify sequence.

# Combined phase-3 apply is intentionally rejected.
```

At the end the orchestrator prints the `export PATH` line to add the Go
toolchain. It does **not** edit `~/.bashrc` silently.

## Device integrity receipt

After a successful apply, phase 3d rebuilds and verifies a device integrity
receipt — a canonical-JSON snapshot that binds the device to the contract it
was bootstrapped against. The receipt lives at
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

- macOS `arm64`: locally rehearsable through `seed-clean-device.md`.
- Ubuntu `24.04`/`26.04`: **`NOT_PROVEN`** until produced on a real device
  (completion plan residual #8; stage `C9`). The `example-user-ubuntu-1` device
  is the first concrete Linux rehearsal; its observed evidence closes part of
  that residual but does not by itself accept Linux consumer execution. Its
  device integrity receipt verifies `PROVEN` against the current contract,
  proving the runtime/tool layer is reproducible on Linux even though the full
  release-bound production path is not yet exercised.
- Source-build phase 1 is a development/canary path and is not a release
  artifact. A hosted release remains the production boundary.

## Stop conditions

Stop on untrusted artifact, unsupported platform, config collision, secret
leak, hook trust failure, stale plan, or any unexpected system write — the
same `release-lifecycle.md` stop conditions, plus the OS-installer's own
failures.

## Authority

`docs/migration/gds-completion-plan.md`. Lower-level contracts:
`docs/contracts/seed-bootstrap-v1.md`, `docs/contracts/bundle-release-v1.md`.
