---
name: gds-drakkars-rollout-consumer
description: Use this skill only for an explicitly approved rollout of a fleet label, public workflow version, or runner behavior to selected consumers. Use phased coexistence, evidence from real consumer jobs, drain evidence, rollback checkpoints, and exact commit pins. Do not use it when active jobs would be cancelled or old capacity removed early.
---

# Contract

Change consumers without losing work or creating an unserviceable label window.

## Use when

- The owner explicitly approves selected consumers moving to a verified product or fleet contract.

## Do not use when

- The product is unverified, work is design-only, or coexistence and rollback are unavailable.

## Inputs

- Exact source and target identities, consumers, waves, gates, and rollback target.

## Preconditions

1. Capture current GitHub, estate, provider, runtime, and Git state and verify public hosted checks.

## Workflow

1. Resolve every affected Git and provider boundary and capture current exact state.
2. Verify the public product commit with hosted checks, then pin it in the private estate.
3. Create new capacity disabled; validate identity, image, labels, limits, and compatibility.
4. Enable the new path without removing the old one. Qualify it only through
   ordinary real jobs created by the selected consumers; never create
   synthetic, benchmark, soak, canary, rerun, or empty-commit traffic.
5. Move consumers in bounded waves while old and new paths coexist.
6. Observe queue, failures, retries, provisioning, teardown, and end-to-end latency after each wave.
7. Disable the old path only when its queued and running intent count is zero. Remove it only after a further verified drain window.
8. Record immutable evidence and preserve a tested rollback checkpoint.

## Stop conditions

Stop on active old-path jobs, missing telemetry, a regression in real consumer
jobs, configuration mismatch, or rollback uncertainty.

## Output

Return phase, exact identities, real-job evidence, drain proof, rollback path,
and next safe action.

## Verification

Prove exact identities, green real consumer jobs, no lost jobs, zero-active
drain, immutable evidence, and rollback.

## References

Use the verified public release, private estate plan, provider inventory, queue journal, GitHub runs, and telemetry.
