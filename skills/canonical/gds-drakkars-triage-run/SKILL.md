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
2. Follow the same identifiers through admission, priority, assignment,
   provisioning, runner registration, setup, execution, upload, and teardown.
   Start from the native job-start log fields (`github_repository`,
   `github_workflow_run_id`, `github_run_attempt`, `github_job_name`,
   `github_workflow_ref`, `github_commit_sha`, `runner_name`, `instance_name`).
   Join traces by `runner_name`: `queue.*` supplies `queue_job_uuid`, and
   provider create/delete supplies `incus_member`.
   If GitHub shows the job but the exact numeric job id is absent from the GARM
   workflow-job store, stop the fleet trace at inbound delivery and check the
   current GitHub Actions incident/queue state. Do not infer capacity or runner
   label failure from a job the fleet never received.
3. Locate the first abnormal stage; distinguish queue pressure, missing capability, infrastructure failure, deterministic test failure, flaky failure, timeout, and observability loss.
4. Compare with adjacent successful runs using the same workflow and toolchain.
5. If a transient infrastructure error is proven, recommend a bounded rerun; do not execute it here.
6. Preserve running jobs and label unsupported claims `NOT_PROVEN`.

## Recovery state

- For a delayed `JobAssigned`, check terminal tombstones before treating a
  completed job as live demand.
- For an assigned job without an instance, inspect the scheduler recovery
  attempt, typed capacity retry, startup grace and cooldown; healthy sibling
  progress is not proof that the exact identity advanced. Raw missing workflow
  or repository identity is expected before the authenticated running claim
  and must not be confused with a running persistent-correlation gap.
- For an `in_progress` job whose runner id disappeared, inspect the durable
  vanished-runner transaction and authoritative `run_attempt`. A force-cancel
  followed by one full rerun is one recovery lifecycle, not two independent
  mutations.
- Distinguish a failed bounded recovery with progressed identities from a
  complete recovery and from a restart storm.

## Direct-JIT identity

Do not require `github_runner_request_id` from a running direct-JIT job. It is
an AcquireJobs identity GitHub does not emit on that path. Require queue UUID,
workflow run, numeric GitHub runner, runner name and instance/member correlation
instead. A missing request ID remains actionable only before the execution path
has converged or when those authoritative identities are also absent.

## Output

Return the causal timeline, first failing stage, supporting links or private evidence locations, impact, and safest recovery path.

## Stop conditions

Stop before cancelling, rerunning, restarting, editing, or treating a retry as proof of repair.

## Verification

Use stable identifiers and distinguish observation, inference, and `NOT_PROVEN`.

## References

Use current GitHub service status and job events, queue intents, terminal
tombstones, scheduler/vanished recovery state, provider leases, runner logs,
traces, OTEL delivery counters and the current OpenObserve alert outcome plus
its silence window.
