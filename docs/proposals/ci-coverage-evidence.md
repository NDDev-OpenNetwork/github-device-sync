# Proposal: read-only CI execution coverage

Status: documentation-only proposal. No evaluator, CLI, collector, enrollment or
runtime behavior is implemented by this branch.

A source implementation was prepared and tested in an isolated local environment,
but the GitHub tool blocked its publication. That write was not retried through
another method. This proposal records the intended contract and the remaining
integration work without claiming that the implementation is in this repository.

## Purpose

Repository discovery, effective runner eligibility and successful CI execution
must be separate facts. A discovered repository is not necessarily within the
right installation scope, runner group, workflow policy or cache authorization.
Do not add coverage evaluation to every job's scheduling path: it belongs in the
read-only control-plane observation/reporting layer.

## Proposed evidence contract

A versioned snapshot should bind the exact estate commit, engine gitlink commit
and module-lock SHA-256. Its expected binding must come independently from the
current checked-out source, not be copied from the snapshot being evaluated.

Declare every repository by immutable numeric ID and current full name. Each
active repository declares the required execution profiles. Every profile carries
an exact repository revision, not a mutable branch or runner label. Account for
renames and transfers without silently treating old-name evidence as current.
Archived repositories are explicitly excluded; an empty or all-archived scope
must not pass as complete CI coverage.

Evaluate fresh, attributable observations for installation scope, effective
Actions policy, runner-group access, actual OS/architecture/label compatibility,
and reusable-workflow eligibility. Include cache authorization when that profile
requires it. Check results must distinguish allowed, denied and unknown. Missing,
stale, future, incomplete-pagination and unreadable evidence is unknown, not an
implicit allow. Normalize inherited restrictions and workflow pin constraints
where supported before producing the snapshot.

Execution evidence is separate: exact repository, profile, revision, run ID,
job ID, attempt, head SHA, conclusion, completion time and provenance reference.
A prior successful job can coexist with newly revoked runner access. Cancellation,
wrong revision, stale success or another repository's job must not establish
current execution. Collector normalization must verify all cross-object joins.

Provenance references are locators, not authenticated attestations. A validator
can check structure, source binding, uniqueness and freshness without proving
that the input record is true. Protected collection and actual source retrieval
remain necessary. No observer should receive broad mutation permissions merely
to make coverage reporting simpler.

## Intended implementation boundary

A reusable standard-library-only evaluator and a standalone JSON-in/JSON-out
inspector were proposed. A separate command avoids changing the established
Cobra command/capability registry before its application-layer integration is
reviewed. No such command is available from this documentation-only change.

Desired reporting properties: deterministic per-repository/profile rows with
expected revision, bounded reason codes, separate eligibility and execution
statuses, no side effects, explicit incomplete inventory, and meaningful process
exit codes. Bound input size and nesting; reject duplicate fields, concatenated
JSON, ambiguous identities and unknown statuses rather than last-value-wins
parsing of permission evidence.

## Local exploration and missing verification

The unpublished local prototype's two packages passed six top-level Go tests
with 36 named table subcases, plus strict decoder cases, under Go 1.23.2 with the
race detector; focused go vet also passed. The temporary module used only the
standard library and did not change the repository's pinned toolchain. These
results do not verify code in this branch, authenticate production coverage, or
replace full pinned-toolchain repository CI.

Before implementation is accepted, independently review the prototype and the
contract, implement the reviewed changes through an authorized development
workflow, and test the actual committed source. Required cases include partial
inventory, missing installation proof, runner-group denial, stale and future
observations, renames, revision mismatch, cancellation, missing required cache,
identity collisions, JSON ambiguity, stable ordering and process exit behavior.

## Remaining integration and acceptance

- Complete authenticated, paginated collection from the canonical repository
  inventory and effective forge policies; preserve raw evidence privately.
- Integrate declarations and evaluated observations through the existing
  application/control-plane boundaries without a second policy authority.
- Explicitly reconcile new, renamed, transferred, archived and revoked repos.
- Store observed state outside tracked desired configuration and bind it to exact
  source identities; keep public examples synthetic.
- Exercise real jobs for every required private-repository profile and report
  eligibility, observed execution and missing evidence independently.
- Preserve normal tenant isolation, credential scoping and cache trust boundaries;
  do not reinterpret missing access as permission to grant it automatically.

Relevant official contracts for a future collector:

- https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/manage-access
- https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/use-in-a-workflow
- https://docs.github.com/en/rest/actions/workflow-jobs
