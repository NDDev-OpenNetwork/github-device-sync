---
name: gds-triage-estate-drift
description: Use this skill in a control-plane or portfolio session when detected estate drift must be classified, deduplicated, prioritized, and routed to canonical owners. Produce remediation candidates without applying them. Do not use it for ordinary repository implementation or silent auto-fix.
---

# Contract

Turn drift evidence into an ordered, non-mutating remediation queue.

## Use when

- Reconciliation or validation produced multiple drift findings.
- The owner needs blast-radius, severity, or canonical-owner analysis.

## Do not use when

- No current drift evidence exists.
- The request is to apply a specific remediation.

## Inputs

- Drift report, inventory version, and observation timestamps.
- Effective policies and active exceptions.

## Preconditions

1. Preserve `UNKNOWN` and `NOT_PROVEN` states.
2. Do not convert access failures into lifecycle conclusions.

## Workflow

1. Deduplicate findings by object, field, desired digest, and observed digest.
2. Resolve the canonical owner and applicable policy for each finding.
3. Classify security impact, blast radius, reversibility, and approval class.
4. Group related findings into candidate plans.
5. Recommend canary order and evidence needed before mutation.

## Stop conditions

Stop before remediation, suppression, or exception creation.

## Verification

Ensure every item retains original evidence and maps to exactly one canonical
owner or is explicitly marked unresolved.

## Output

Return prioritized drift groups, owners, severity, affected targets, evidence
gaps, and suggested planning workflows.

## References

Estate drift includes `GDS_RUNTIME_DEPENDENCY_PIN_DRIFT` from `gds validate
estate`, raised when a runtime module's `pinned_sha` in
`estate/runtime-dependencies.yaml` diverges from the consuming submodule's
in-tree contract; its canonical remediation is `gds-update-consumer-pins`.
Otherwise no additional runtime reference is required; retain source evidence in
every finding.
