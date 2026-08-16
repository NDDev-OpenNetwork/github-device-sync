---
name: gds-audit-estate
description: Use this skill in a control-plane admin session when the owner asks for a read-only audit across repositories, portfolios, installations, policies, projections, or rollout state. Aggregate bounded evidence without applying remediation. Do not use it for one-repository review or any external mutation.
---

# Contract

Aggregate a bounded, read-only estate audit from desired and observed evidence.

## Use when

- The owner requests estate-wide health, compliance, adoption, or drift evidence.
- A rollout or governance change needs a baseline.

## Do not use when

- The scope is one repository.
- The request authorizes applying fixes.

## Inputs

- Control-plane estate root.
- Target selectors, freshness requirements, and maximum scope.

## Preconditions

1. Resolve the control-plane role with `gds context --json`.
2. Use read-only credentials and bounded concurrency.
3. Treat inaccessible repositories as access failures, not deletions.

## Workflow

1. Compile desired inventory and selectors.
2. Read current observed state within declared freshness limits.
3. Run applicable estate validators.
4. Classify drift by object, authority, severity, and proof state.
5. Isolate individual repository failures from aggregate results.

## Stop conditions

Stop before any repository, provider, workflow, ruleset, or rollout mutation.

## Verification

Require a stable inventory version, observation timestamps, and per-target
result counts that reconcile to the requested scope.

## Output

Return scope, coverage, confirmed findings, inaccessible and stale targets,
rate-limit evidence, and non-mutating next actions.

## References

Per ADR 0025, a checkout under the out-of-estate `${HOME}/Developer/external`
root yields exactly one expected `GDS_WORKSPACE_ANCHOR_REQUIRED` finding with
`anchor_state: missing` in `gds workspace audit`. Report it as accepted steady
state rather than remediable drift, and do not treat it as coverage loss.
Placement findings for the same path are not expected and remain real drift.

`GDS_CONTEXT_IDENTITY_CONFLICT` and `GDS_IDENTITY_INDEX_ID_CONFLICT` on one
repository ID mean the same repository has both a gitlink and a standalone
checkout. Per ADR 0027 the standalone copy is the defect; report it as removable
only after confirming its commits already exist in the submodule's Git store.

`GDS_ESTATE_SELECTOR_CONFLICT` means two selectors tied at the highest matching
priority for one repository, leaving it `unassigned`. Selectors follow a
priority-band convention: `100` for generic source/fork classification, `200`
for specialized non-fork overrides (servers, named-prefix families), and `300`
for state overrides that outrank topology and name (archived). The archived
state takes precedence over fork topology and the `server-*` name, so a
provider-archived repository resolves to `portfolio:archived-projects`
regardless of its fork flag or name prefix. See `docs/contracts/estate-v1.md`
for the full precedence rule.

Otherwise no additional runtime reference is required; use current structured
`gds` evidence.
