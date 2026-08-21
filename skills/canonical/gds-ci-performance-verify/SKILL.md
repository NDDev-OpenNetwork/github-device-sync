---
name: gds-ci-performance-verify
description: Use this skill when the owner asks whether a CI or fleet change actually improved speed or reliability. Compare equivalent runs using end-to-end, queue, provisioning, setup, execution, and deploy timing plus failures and retries. Do not use it to tune, rerun, cancel, or deploy.
---

# Contract

Verify performance with comparable evidence rather than isolated job duration.

## Use when

- A change has before and after evidence that needs performance verification.

## Do not use when

- The request is implementation, current-health audit without baseline, or diagnosis of one run.

## Inputs

- Change identity, before and after windows, repositories, workflows, priorities, and success criteria.

## Preconditions

1. Define the user-visible interval and verify clocks, correlation, and cohort usability.

## Workflow

1. Define the user-visible interval, normally PR-ready to all required checks and deployment complete.
2. Select before/after cohorts matched by repository, workflow, event, change size, cache state, platform, and priority.
3. Decompose median, p90, and p95 latency into queue, provision, setup, execute, upload/deploy, and teardown stages.
4. Compare throughput, utilization, success, retry, infrastructure-failure, flaky-failure, and missing-telemetry rates.
5. Identify critical-path changes and resource contention; keep priority classes separate.
6. Reject conclusions with insufficient samples or incomparable cohorts and label them `NOT_PROVEN`.

## Output

Return cohort definition, metric table, confidence limits or evidence limits, regressions, improvements, reliability impact, and the next measurement needed.

## Stop conditions

Stop before rerunning work, changing configuration, or claiming causality from unmatched evidence.

## Verification

Recompute cohort membership and metrics from authoritative events and state sample and confidence limitations.

## References

Use current GitHub run events and correlated private fleet observability evidence.
