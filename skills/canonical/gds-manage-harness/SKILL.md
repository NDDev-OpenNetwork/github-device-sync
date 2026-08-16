---
name: gds-manage-harness
description: Use this skill only when the owner explicitly asks to add, update, verify, migrate, or retire an agent harness adapter in GDS. Research current official behavior, update one capability profile and generated projections, and prove runtime discovery. Do not use it to change a user's global harness configuration silently.
disable-model-invocation: true
---

# Contract

Change one harness adapter from current official evidence with reversible local
and rollout behavior.

## Use when

- A supported harness or its discovery, skills, hooks, or config behavior changed.

## Do not use when

- The task is ordinary repository work.
- Only a device-local install is requested; use the device workflow.
- The owner is changing which harnesses one device runs rather than the harness
  contract itself. That is a device selection edit: change `harnesses:` in the
  device descriptor, then reconcile with `gds harness sync --device <path>`.

## Inputs

- Harness product, exact version or tested range, and requested lifecycle change.
- Official source register entries and runtime test environment.

## Preconditions

1. Inspect current official product documentation.
2. Mark changed but unreviewed claims stale.
3. Run `gds harness <operation> --plan` and obtain approval for installs/removals.

## Workflow

1. Update the canonical capability profile and adapter source.
2. Regenerate harness projections from canonical skills and policies.
3. Run clean-environment root, nested, standalone, and embedded discovery tests.
4. Verify explicit-only behavior and public/private boundaries.
5. Prepare canary rollout and rollback.

## Stop conditions

Stop on unofficial evidence, unsupported product behavior, stale profile,
runtime discovery failure, trust failure, or private-context leakage.

## Verification

Run `gds validate harnesses --harness <id>` and the adapter runtime suite. When
the change adds or retires a catalogue entry, also run
`gds harness sync --device <path>` for each affected device: it reports which
installed projections no longer match that device's declared selection.

## Output

Return verified sources, profile delta, generated artifacts, runtime evidence,
limitations, and rollout/rollback state.

## References

Read only the official sources named by the current harness capability profile.

GDS keeps two separate facts. The catalogue —
`harnesses/capability-registry.yaml` plus `core/harness.CanonicalIDs` — is every
harness GDS can render, and grows when a harness is added. The device selection
— `harnesses:` in `estate/devices/<device>.yaml` — is the subset one device
actually runs, and is usually two or three. Static contracts apply to the whole
catalogue; runtime evidence is required only for the selected set, so a
provisional catalogue entry never blocks a device that does not run it.
