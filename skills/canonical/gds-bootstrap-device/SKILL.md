---
name: gds-bootstrap-device
description: Use this skill only when the owner explicitly asks to install or verify GDS, its pinned bundle, and selected harness projections on a new macOS, Linux, server, or CI device. Build an exact reversible plan and keep secrets in approved stores. Do not use it to clone every estate repository or change global tools silently.
disable-model-invocation: true
---

# Contract

Create a reproducible device control-plane installation from a trusted
immutable bundle.

## Use when

- A new device or server must be prepared for selected GDS workflows.

## Do not use when

- Only repositories need materialization.
- Device-local installation approval is absent.

## Inputs

- Device identity/profile, OS/architecture, bundle identity, and selected harnesses.
- Approved secret references and installation roots.

## Preconditions

1. Inspect existing tools, paths, configs, and conflicts without changing them.
2. On a device with no `gds`, complete `docs/runbooks/seed-clean-device.md`
   first: it acquires the pinned bootstrap at an exact commit, establishes an
   independently authenticated seed verifier, and splits the downloaded release
   into the six-file release directory and the three-file evidence directory.
   Verification runs under that seed until a verified release binary is
   installed.
3. Verify bundle digest, provenance, release sequence, and platform support with
   `gds release verify`.
4. Produce the installation plan with `gds release install --plan` and obtain
   approval before any mutation.

## Workflow

There is no single `bootstrap` verb; a device is brought up through the staged
release, estate, and harness command families and approved at each apply.
The phased orchestrator `scripts/bootstrap-device.sh` sequences these
boundaries; run `--plan` first, then `--apply`.

## Interactive sudo requirements

Some phases require **interactive sudo** (the user's password). The agent
cannot enter it. Present these steps as exact commands the owner runs in a
terminal, then wait for confirmation before continuing.

1. **OS bootstrap (phase 2)** installs system packages via apt and (on
   desktop) configures GNOME, keyboard, BrowserOS, and removes stock Firefox.
   The owner runs:
   ```
   scripts/bootstrap-device.sh --device estate/devices/<device>.yaml --apply --from-phase 2
   ```
   The script prompts for sudo and handles the rest. Desktop customization
   (`scripts/ubuntu/desktop.sh`) is called automatically for the `desktop`
   profile with `gui: enabled`; each step is independent and idempotent.

2. **Harness module installs (phase 2, inside OS bootstrap)** may install deb
   packages (e.g. ZCode). The OS bootstrap script prompts for sudo once and
   keeps it fresh per-step.

3. If a step fails on expired sudo, re-running the orchestrator resumes from
   the failed phase (`--from-phase N`).

After the owner confirms the sudo-requiring phases completed, the agent
continues with the control-plane staged commands (phase 3), which do not
require sudo.

1. Install the pinned immutable bundle: `gds release install --apply <plan> --approval-ref <ref>`,
   then `gds release install --verify <operation-id>`.
2. Register the device-local control plane: `gds workspace register-estate --plan`,
   then `--apply <plan> --approval-ref <ref>`, then `--verify <operation-id>`.
3. Generate device-local harness projections for the selected harnesses only:
   `gds harness render` then `gds harness sync --converge` — without modifying canonical
   sources. The selection is `harnesses:` in the device descriptor, not the whole
   catalogue; `gds harness sync --device <path>` names exactly which harnesses
   this device still needs installed, updated, or removed.
4. Run GDS and harness doctor tests.
5. Preserve an exact uninstall/rollback record.

## Stop conditions

Stop on untrusted artifact, unsupported platform, config collision, secret leak,
hook trust failure, stale plan, or unexpected system write.

## Verification

Verify CLI identity, bundle lock, harness discovery, explicit-only skills,
permissions, and rollback in an isolated test where possible.

## Output

Return installed paths and versions, trust evidence, selected profiles,
runtime tests, user trust actions, and rollback instructions.

## References

Use only the pinned bundle, device profile, and selected harness profiles.

The phased bootstrap orchestrator `scripts/bootstrap-device.sh` sequences the
three boundaries below (OS bootstrap, seed Go+gds, control-plane staged) and
derives the `macos-ubuntu-bootstrap` OS-installer flags from the device
descriptor's optional `class:` block. Run `--plan` first, then
`--apply --approval-ref <ref>`; `--from-phase N` resumes, `--phase N` runs one
phase. See `docs/runbooks/bootstrap-device.md` for the full seam and the
device-class contract.

Changing which harnesses a device runs is a device change, not a harness-contract
change: edit `harnesses:` in `estate/devices/<device>.yaml`, then reconcile.
`gds harness sync --device <path> --target-root <root>` is read-only and
classifies every catalogue entry as install, update, remove, or current; adding
`--converge --approval-ref <ref>` applies it as an ordered sequence of the
existing single-harness transactions. That sequence is **not atomic** — a stopped
run reports `GDS_HARNESS_SYNC_PARTIAL`, applied steps stay applied, and re-running
resumes.

Sync refuses ambiguous input instead of guessing, and each refusal means
something different: `GDS_HARNESS_SELECTED_UNKNOWN` (an id the catalogue does not
have), `GDS_HARNESS_SELECTION_EMPTY` (a blank list, which must not be read as
"remove everything"), `GDS_HARNESS_TARGET_COLLISION` (two selected harnesses whose
skill roots overlap cannot share one target root — resolve by deselecting one or
giving them separate roots), and `GDS_HARNESS_UNOBSERVABLE_NOT_PROVEN` (an
unselected entry whose state cannot be read, so a leftover install of it would go
unreported).

The catalogue is every harness GDS can render; the device selection is the two or
three it runs. Never add catalogue entries to a device to "register" them.
