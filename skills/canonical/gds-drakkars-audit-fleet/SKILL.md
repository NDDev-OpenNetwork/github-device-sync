---
name: gds-drakkars-audit-fleet
description: Use this skill for a read-only audit of CI fleet health, queue latency, capacity, runner availability, tool compatibility, retries, logs, telemetry, or failure history. Correlate evidence across control plane, GitHub, runners, and observability. Do not use it to deploy, restart, cancel, rerun, resize, or edit configuration.
---

# Contract

Explain fleet health and bottlenecks from correlated, time-bounded evidence.

## Use when

- The owner needs fleet-wide capacity, queue, compatibility, log, or telemetry evidence.

## Do not use when

- The scope is one run, one workflow, or an explicitly requested mutation.

## Inputs

- Audit window, fleet context, priority or pool filters, and technology scope.

## Preconditions

1. Resolve GDS context and the private estate source without copying private facts into public artifacts.

## Workflow

1. Resolve GDS context and the private estate source without copying its facts into public outputs.
2. Establish the audit window and inventory expected pools, capacity, priority, and technologies.
3. Correlate GitHub queue/start/end events with scheduler, provider, host, runner, and telemetry records using stable run, job, intent, and instance identifiers.
4. Measure end-to-end latency, queue time, provisioning, setup, execution, teardown, utilization, failure, retry, and orphan rates by pool and priority.
   Under contention, verify that one repository uses no more than 75 percent
   of slot, measured CPU, and measured memory capacity; without a competing
   repository, verify that it may use the full fleet.
5. Check support for every detected toolchain and package manager; classify unsupported-tool failures separately from project defects.
6. Check log completeness, redaction, retention, clock alignment, and missing
   correlation fields. Distinguish the bounded pre-reconciliation account-only
   window from intent records that remain unbound after authoritative queued
   and already-running reconciliation.
   Treat `gha_fleet_queue_missing_runner_request_id` as incomplete
   pre-execution correlation only. A running direct-JIT job with UUID,
   workflow-run, numeric GitHub-runner and runner-name identity belongs to
   `gha_fleet_queue_direct_jit_without_runner_request_id`; GitHub emits no
   AcquireJobs request ID for that path.
7. In OpenObserve, query job-start logs by `msg = 'job correlation accepted'`.
   Join `github_*`, `runner_name` and `instance_name` to `fleet_traces` using
   trace search type `traces`; queue spans expose `queue_job_uuid`, while
   provider spans expose `incus_member`. Do not use the provider process
   resource host as the compute placement member.
8. Separate confirmed faults, saturation, waste, and `NOT_PROVEN` gaps.

## Safety

Never expose private topology or tenant identifiers in a public artifact. Never treat a retry as proof that the cause disappeared.

## Output

Return prioritized findings, evidence, latency decomposition, compatibility gaps, capacity risks, and non-mutating remediation options.

## Stop conditions

Stop before restart, retry, cancellation, deployment, resize, or configuration write.

## Verification

Cross-check GitHub, runtime journals, provider inventory, hosts, and observability freshness; mark gaps `NOT_PROVEN`.

## References

Use current structured GDS, GitHub, provider, host, and observability evidence.
