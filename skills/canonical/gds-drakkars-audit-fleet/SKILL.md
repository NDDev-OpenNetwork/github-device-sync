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
   Before attributing queued jobs to fleet capacity, check GitHub Actions
   service status and prove whether the exact `workflow_job` reached the
   control-plane database. A GitHub job with no corresponding row is inbound
   event-delivery delay, not a missing runner or exhausted pool.
4. Measure end-to-end latency, queue time, provisioning, setup, execution, teardown, utilization, failure, retry, and orphan rates by pool and priority.
   Under contention, verify that one repository uses no more than 75 percent
   of slot, measured CPU, and measured memory capacity; without a competing
   repository, verify that it may use the full fleet.
5. Check support for every detected toolchain and package manager; classify unsupported-tool failures separately from project defects.
6. Check log completeness, redaction, retention, clock alignment, and missing
   correlation fields. Queued and assigned authoritative rehydration may retain
   raw missing/unbound identity while waiting for capacity or a runner claim;
   persistent correlation is actionable only after a running intent exceeds
   its own state-entry grace.
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
8. Verify the telemetry plane by role: OpenTelemetry collection and transform,
   OTLP transport, and OpenObserve storage/query/alerts. PromQL is an
   OpenObserve query language, not evidence of a Prometheus server. Check every
   declared collector's disk queue, refused/send-failure counters and restart
   state, plus an external heartbeat that does not depend on OpenObserve.
   Treat classified LVM, overlay, audit, firewall and workqueue metrics as
   bounded host-signal evidence; their raw high-volume logs need not be copied
   into the application stream.
9. Check host compliance coverage for package inventory freshness,
   reboot-required, running kernel and kernel-reported vulnerability state.
   Keep hardware/microcode boundaries separate from software drift.
10. Read alert outcome, last-satisfied time and configured silence together.
   OpenObserve v0.92 pauses outcome evaluation during silence, so a recovered
   expression may retain an older firing outcome until that bounded window.
11. Separate confirmed faults, saturation, waste, and `NOT_PROVEN` gaps.
12. Inspect durable lifecycle recovery rather than process health alone:
   terminal job tombstones, overdue non-terminal provider retries,
   assigned intents with an exact workflow-job row but no instance, scheduler
   recovery startup grace/cooldown/active attempt, and vanished-runner recovery
   transactions. An incomplete bounded attempt is evidence of partial progress,
   not success and not permission for an immediate duplicate restart.

## Runner scale sets

For Runner Scale Set V2, `runs-on` can target `RunnerScaleSetName`. An empty
classic-label array from the self-hosted runner REST endpoint does not by itself
prove an unserviceable runner. Correlate the requested target with the runner's
scale-set id/name and the scale-set listener before diagnosing label drift or
proposing classic-label mutation.

## Safety

Never expose private topology or tenant identifiers in a public artifact. Never treat a retry as proof that the cause disappeared.

## Output

Return prioritized findings, evidence, latency decomposition, compatibility gaps, capacity risks, and non-mutating remediation options.

## Stop conditions

Stop before restart, retry, cancellation, deployment, resize, or configuration write.

## Verification

Cross-check GitHub service status, exact workflow-job delivery, runtime
journals, recovery state, provider inventory, runner scale-set identity, hosts,
collector delivery counters, external backend heartbeat, alert outcome
freshness, compliance coverage and observability freshness; mark gaps
`NOT_PROVEN`.

## References

Use current structured GDS, GitHub, provider, host, and observability evidence.
