---
name: gds-rollout-policy
description: Use this skill only when the owner explicitly asks to roll out an immutable GDS bundle or policy version across managed repositories. Plan canaries and bounded waves, apply approved repository changes, pause on failures, and verify adoption. Do not use it to edit canonical policy or publish a bundle.
disable-model-invocation: true
---

# Contract

Roll out one verified immutable bundle through canaries and bounded waves.

## Use when

- A released bundle must be adopted by selected managed repositories.

## Do not use when

- The bundle is not released, trusted, or verified.
- The request is to author policy or release the control plane.

## Inputs

- Exact source and target bundle identities and digests.
- Target selector, rollout rings, gates, and approval.

## Preconditions

1. Verify artifact identity, digest, attestation, release sequence, and rollback
   target.
2. Run `gds rollout plan --file <rollout-request>` and present exact target
   repositories and writes.
3. Obtain approval for the concrete rollout plan.

## Workflow

1. Apply canary subplans.
2. Verify projections, harness discovery, required checks, and security gates.
3. Advance one bounded wave only when all gates pass.
4. Pause immediately on security or threshold failure.
5. Reconcile final adoption state.

## Stop conditions

Stop on stale plan, changed target state, trust failure, missing rollback,
unexpected generated diff, wave failure, or revoked approval.

## Verification

`gds rollout` plans only and has no apply or verify mode. After each wave and at
completion, verify each target with its own operation-specific verify command,
then re-run `gds rollout plan --file <rollout-request> --json` to reconcile
remaining adoption.

## Output

Return per-wave results, adoption counts, paused targets, evidence, and exact
rollback or continuation state.

## References

`rollout_ring` is a schema-enforced enum, not a free-form identifier. The legal
values are `standard`, `canary-control-plane`, `canary-modules`, and
`quarantine`, defined in `schemas/v1/common.schema.json#/$defs/rolloutRing` and
referenced from the `selector`, `owner`, `repository`, and `estate` schemas. A
typo now fails validation. The field is descriptive metadata; no Go logic
branches on it. It is distinct from the rollout-request `channel` enum
(`canary`, `stable`, `frozen`) which selects the release channel.

Otherwise no additional runtime reference is required; the rollout plan carries
its gates and rollback.
