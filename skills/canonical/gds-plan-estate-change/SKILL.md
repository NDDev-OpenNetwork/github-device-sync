---
name: gds-plan-estate-change
description: Use this skill in a control-plane admin session when the owner wants a structured, side-effect-free plan for a change spanning repositories, portfolios, policies, harnesses, or GitHub governance. Resolve targets, dependencies, approvals, waves, and rollback. Do not use it to apply the plan.
---

# Contract

Build a reproducible estate-change plan without external mutations.

## Use when

- One logical change affects multiple independent repositories.
- Risk, rollout rings, approvals, or compensations must be designed first.

## Do not use when

- The request is only an audit.
- The owner has asked to apply an already verified plan.

## Inputs

- Desired change and explicit target selector.
- Current desired and observed state.
- Approval, rollout, and rollback policies.

## Preconditions

1. Resolve control-plane identity.
2. Refresh decision-relevant evidence or mark it stale.
3. Keep planning commands side-effect-free.

## Workflow

1. Resolve exact target repositories and independent Git boundaries.
2. Build dependency order and per-repository subplans.
3. Define preconditions, approval classes, canaries, waves, pause gates, and
   compensations.
4. Validate the plan schema and digest.
5. Present all intended external writes separately.

## Stop conditions

Stop if target selection is ambiguous, policy or access is unknown, rollback is
undefined for a risky step, or any planning command would mutate state.

## Verification

Validate the plan and prove that every step targets one declared repository and
has matching preconditions.

## Output

Return the plan ID and digest, target set, steps, gates, approvals, rollback,
unknowns, and expiry. Do not apply it.

## References

No additional runtime reference is required; the validated plan is the handoff artifact.
