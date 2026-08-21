---
name: gds-drakkars-triage-run
description: Use this skill when one CI run or job is slow, queued, retried, stuck, or failed and the owner wants the cause. Trace it end to end with GitHub and fleet telemetry. Report evidence and safe recovery choices. Do not use it to cancel, rerun, restart, or modify the workflow.
---

# Contract

Trace one run from event receipt to terminal result without changing state.

## Use when

- One identifiable run or job needs a causal explanation.

## Do not use when

- The request covers fleet-wide capacity, all workflows, or asks to apply recovery.

## Inputs

- Repository, run and attempt or job identity, plus the relevant time window.

## Preconditions

1. Confirm the GitHub identity and preserve current running state.

## Workflow

1. Capture repository, run, attempt, job, event, requested labels, commit, and timestamps.
2. Follow the same identifiers through admission, priority, assignment, provisioning, runner registration, setup, execution, upload, and teardown.
3. Locate the first abnormal stage; distinguish queue pressure, missing capability, infrastructure failure, deterministic test failure, flaky failure, timeout, and observability loss.
4. Compare with adjacent successful runs using the same workflow and toolchain.
5. If a transient infrastructure error is proven, recommend a bounded rerun; do not execute it here.
6. Preserve running jobs and label unsupported claims `NOT_PROVEN`.

## Output

Return the causal timeline, first failing stage, supporting links or private evidence locations, impact, and safest recovery path.

## Stop conditions

Stop before cancelling, rerunning, restarting, editing, or treating a retry as proof of repair.

## Verification

Use stable identifiers and distinguish observation, inference, and `NOT_PROVEN`.

## References

Use current GitHub job events, queue intents, provider leases, runner logs, and traces.
