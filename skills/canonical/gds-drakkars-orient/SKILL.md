---
name: gds-drakkars-orient
description: Use this skill when the owner asks where CI or Drakkars behavior is owned, which runner policy applies, or how public product code and private estate facts are separated. Resolve verified context without changing anything. Do not use it for a deep audit, optimization, rollout, or provider write.
---

# Contract

Resolve the current CI/fleet context and its authority boundaries without mutation.

## Use when

- The owner asks which CI or fleet authority, visibility rule, or Git boundary applies.

## Do not use when

- The request needs a deep audit, run trace, implementation, onboarding, or rollout.

## Inputs

- Repository path or stable identity and the owner's context question.

## Preconditions

1. Work from the relevant Git boundary and keep remote facts `NOT_PROVEN` until refreshed.

## Workflow

1. Run `gds context --json` at the relevant Git boundary.
2. Identify repository visibility, active profiles, module pins, and canonical owner.
3. Treat portable engines, reusable workflows, schemas, and generic skills as public product concerns.
4. Treat organizations, repository identities, priorities, hosts, networks, credentials, and runtime evidence as private estate concerns.
5. Mark unfetched provider or telemetry facts `NOT_PROVEN`.
6. Treat Runner Scale Set V2 names as routing targets. Do not equate an empty
   classic self-hosted-label response with a missing scale-set name or listener.
7. Recommend the narrowest next skill and Git boundary.

## Stop conditions

Stop before edits, fetch-based conclusions, workflow dispatch, runner changes, or provider writes.

## Output

Return verified context, independent mutation boundaries, visibility constraints, evidence gaps, and the safe next workflow.

## Verification

Every stated fact names its verified source and public output contains no private topology or tenant detail.

## References

Use current structured `gds context --json` output and the active repository policy.
