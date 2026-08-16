# ADR 0022: Canonical GitHub governance policy and read-only comparison

Status: Accepted

Date: 2026-07-11

## Context

Typed GitHub observations are not desired state. Conversely, a desired value
without an ownership mode can cause a reconciler to mutate fields that should
only be reported. GitHub Actions `allowed_actions: selected` is also incomplete
without the separate selected-actions object.

## Decision

- Canonical repository governance lives in reusable policy sources under
  `apply.github`; provider snapshots and generated reports never own intent.
- Every declared setting is `managed`, `observed`, or `ignored`. Only managed
  settings carry a desired value and may become a future mutation target.
- Merge, repository Actions, and workflow-token defaults are independently
  typed. Security analysis and effective rulesets remain observed until their
  complete plan-specific contracts are modeled.
- The base policy manages whether Actions is enabled and the default workflow
  token permissions. It observes `allowed_actions`, SHA-pinning enforcement,
  and selected-actions permissions until an owner or portfolio policy defines
  a complete allowlist for every required workflow and action. Enforcing an
  incomplete estate-wide allowlist is forbidden because it can disable valid
  repository CI.
- Selected Actions are one atomic value: GitHub-owned allowance, verified
  creator allowance, and the complete bounded pattern list. The provider reads
  this endpoint only when `allowed_actions` is `selected`.
- `gds github governance --compare-local` requires an exact immutable provider
  ID and owner/name match, compiles the local policy, and returns deterministic
  field-level status. It performs no mutation.
- Policy arrays carry provenance for every element. A replacement removes old
  descendant provenance before recording the new leaves.
- No Mutation App credential, write provider, or remediation handler is added
  by this decision.

## Consequences

- Observed evidence, desired intent, and future mutation plans remain separate.
- A selected-actions policy cannot silently depend on an unrecorded allowlist.
- Repository onboarding cannot turn an incomplete base allowlist into a future
  destructive reconciliation target.
- False compliance against a different local repository is blocked as a
  security error.
- Policy changes alter compiled and projection digests and therefore require a
  controlled regeneration rollout.

## Verification

- Schema fixtures validate the governance setting shapes.
- Compiler tests cover deterministic element-level provenance.
- Provider tests cover the conditional selected-actions read, normalization,
  request evidence, and bounds.
- Comparator tests cover managed compliance/drift and observed/ignored fields.
- App tests cover observed-only behavior and exact identity enforcement.

## Rollback

Revert the policy fields, comparator, and conditional selected-actions reader
together, then regenerate projections from the previous immutable inputs. No
GitHub state rollback is required because this stage is read-only.
