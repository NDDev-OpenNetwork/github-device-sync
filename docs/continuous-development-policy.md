# Continuous development policy

Add `continuous-development` to the repository anchor's `policy.profiles` to
select the public policy in `policies/stacks/continuous-development.yaml`.
The effective leaf is `delivery.profile: continuous-development`; omission or
`standard` retains existing behavior. A higher-priority repository policy may
set `standard` explicitly.

This profile makes broad GitHub CI asynchronous evidence. Generated repository
instructions still require relevant checks and project-owned build, migration
and runtime contracts. Workflows keep their real conclusions; the profile does
not insert `continue-on-error`, skips or fabricated successful statuses.

## Ruleset reconciliation

The normal `gds github ruleset` workflow reads the tracked
`.github/rulesets/branch-main.json` contract and any declared external required
checks. The compiled profile then removes the `required_status_checks` rule from
the desired rules and sets `remove_required_status_checks: true` in the immutable
GDS plan. The plan also binds the compiled-policy digest and observed ruleset.
The internal removal field is never sent to GitHub.

An ordinary omitted rule remains preserved. Explicit removal drops status checks
from the selected repository ruleset, including externally declared contexts in
that rule. Pull-request rules, signatures, other existing rules, conditions and
bypass actors remain under their existing ownership contract. Ordinary updates
also retain unknown provider parameters on status-check and pull-request rules.
Conflicting removal plus desired status checks is rejected before writing.

An existing checks-only ruleset may become an empty ruleset. Readback requires an
explicit empty array, not missing or null rules. When no matching ruleset exists
and removing checks leaves no desired rules, the planner returns in-sync without
creating an empty shell. Apply verifies the resulting state; replay does not
write again, and verification fails if required checks reappear.

Selection alone changes neither live GitHub settings nor application delivery.
Provider writes still use the normal exact-plan approval and journal contract.
Review the concrete selected ruleset before applying removal. Other repository
or organization rulesets, classic branch protection, required workflows and
environment gates are separate contracts: this profile does not assert they
were removed or that a branch can merge without any other requirements.

Applications own deployment executors and their affected checks. CI feedback may
publish actionable failures to the owning repository; this profile does not
launch repair agents or configure a deployment service.

GitHub describes the status-check requirement in
[available rules](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets)
and the update payload in the
[repository rules REST API](https://docs.github.com/en/rest/repos/rules#update-a-repository-ruleset).
