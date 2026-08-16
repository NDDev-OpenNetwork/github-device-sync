---
name: gds-update-consumer-pins
description: Use this skill only when the owner explicitly asks to update verified consumers to a finalized module commit, version tag, or package version. Discover typed consumers, plan independent repository changes, verify each consumer, and roll out safely. Do not use it to release the module or mass-update without a selector.
disable-model-invocation: true
---

# Contract

Update selected consumers to one policy-eligible module artifact through
independent repository transactions.

## Use when

- A finalized module artifact should be adopted by known consumers.

## Do not use when

- The module commit or package is not published and eligible.
- The target consumers are not explicitly selected.

## Inputs

- Module repository ID and exact eligible artifact.
- Consumer selector, compatibility evidence, rollout rings, and approval.

## Preconditions

1. Verify artifact publication, reachability, compatibility, and provenance.
2. Resolve current consumers from typed relationships.
3. Run `gds module update-consumers --plan` and obtain approval.

## Workflow

1. Apply representative canary consumer updates.
2. Run each consumer's required verification.
3. Advance bounded waves only when gates pass.
4. Preserve consumers intentionally pinned to older compatible artifacts.
5. Reconcile final pins and open work.

## Stop conditions

Stop on unverified artifact, unknown consumer policy, failing consumer checks,
unexpected lockfile changes, stale plan, a relationship delegated through
`pin_management: consumer-transaction`, or wave threshold failure. A delegated
consumer must use and verify its repository-owned atomic transaction; never
fall back to a generic gitlink-only update.

## Verification

Run `gds module update-pin --verify <operation-id> --json` per consumer.
`gds module update-consumers` plans only and has no apply or verify mode.

## Output

Return artifact identity, selected consumers, per-consumer pin and checks,
skipped targets, failures, and rollout continuation state.

## References

For a runtime module tracked outside git submodules, the consumer pin is
`pinned_sha` in `estate/runtime-dependencies.yaml`. `gds validate estate`
equality-checks it against the consuming submodule's in-tree contract and fails
on drift (`GDS_RUNTIME_DEPENDENCY_PIN_DRIFT`), so advance that pin in the same
change as the consuming submodule's gitlink and never let the two diverge.

Per ADR 0029 the registry is currently empty and harness applications are out of
estate scope: this estate neither tracks nor verifies their versions, and their
pins are advanced in the consuming repository alone with no estate edit. Do not
re-register them to make a pin visible here. Otherwise use current relationship
and rollout evidence.
