# C10 integrated assurance evidence

Status: intrinsic gates pass; stage remains `implemented-local` until the C6
seventeen-harness runtime prerequisite is accepted.

Evidence date: 2026-07-11

Source commit: `d65ac4b08a937e287e4d2def7ec4292665a4e0f9`

Environment: macOS arm64, Go `1.26.5`, 8 logical CPUs

## Scope

The gate is offline and performs no external mutation. It exercises:

- 2000 repositories across two installations;
- 1000 forks;
- four shared modules and 1000 typed consumers;
- active, maintenance, frozen, and archived lifecycles;
- available, inaccessible, auth-failed, not-found, and unknown access states;
- 1000 webhook deliveries with replay/conflict detection;
- deterministic policy compilation and standalone projection generation;
- SQLite WAL persistence, worker restart, durable reconciliation records, and
  installation-outage isolation;
- one 2000-subplan portfolio plan and four rollout waves;
- rollout pause on a security failure;
- all four kill switches;
- the complete Go core race suite before the production-size scenario.

## Accepted report

```text
assurance_id: assurance_01KX9P398GJKR16VP56KZ3M8A8
source_commit: d65ac4b08a937e287e4d2def7ec4292665a4e0f9
source_worktree_clean: true
result: pass
result_digest: sha256:9878772cfab079e1d6fa1a4c5546b0c9d89064184abe91dd967eaa298961402e
projection_digest: sha256:21237d844e80c39684c343497786213032c1644069f82168ce50c36aa7f0dff1
external_network: false
external_mutations: false
```

The report validates against
`schemas/v1/assurance-report.schema.json`. The runner rejects dirty source,
source changes during execution, duplicate or missing checks/metrics, budget
drift, inconsistent pass state, and digest tampering.

## Measured budgets

| Metric | Observed | Gate |
|---|---:|---:|
| Context p95 | 26.287 ms | <= 2000 ms |
| Repository status p95 | 116.243 ms | <= 2000 ms |
| Inventory compile | 1.213 ms | <= 5000 ms |
| Full reconciliation | 222.721 ms | <= 30000 ms |
| 2000 projection generation | 1042.001 ms | <= 60000 ms |
| Webhook throughput | 3772.816/s | >= 60/s |
| Maximum queue lag | 264.867 ms | <= 30000 ms |
| SQLite restart | 0.674 ms | <= 5000 ms |
| Rollout plan | 4.831 ms | <= 2000 ms |
| Portfolio plan | 59.327 ms | <= 5000 ms |
| Peak heap | 40,113,848 bytes | <= 536,870,912 bytes |
| State database | 7,482,848 bytes | <= 67,108,864 bytes |
| Provider reads per full reconciliation | 2 | <= 2 |

These are measured acceptance ceilings, not provider limits or workload
targets.

The webhook floor was recalibrated on 2026-07-12 after two clean Ubuntu
GitHub-hosted runs on the supported two-CPU profile measured 84.53/s and
82.47/s for the durable sequential SQLite/WAL path. The 60/s floor preserves
roughly 27% headroom below the slower observation while still failing a
material throughput regression. Durability and the measured end-to-end path
were not changed.

## Security and chaos traceability

| Contract | Executable evidence |
|---|---|
| Prompt injection / embedded imperatives | `TestRepositoryProcessorTreatsEmbeddedInstructionsAsOpaqueUntrustedEvidence` |
| Secrets and device-specific paths | `core/security` scanner tests and release public-artifact tests |
| Public/private projection boundary | projection whitelist and private-policy rejection tests |
| Malicious paths and symlink traversal | materializer, projection, state, Git, bundle, and release-consumer tests |
| Command/argument injection | bounded Git runner and remote-name tests |
| Untrusted forks | 1000-fork read/compile scenario plus fork-only commit preservation tests; no repository script executes |
| Webhook signature, replay, conflict, retry, and dead letter | `core/webhooks`, `core/state`, and controller worker tests |
| Token scope and redaction | GitHub permission, expiry, scheduler, response-bound, and token-source tests |
| Workflow supply chain | immutable-ref, permission-expansion, bundle, SBOM, and trusted-root tests |
| Artifact poisoning and rollback | bundle tamper, release-consumer tamper, offline evidence, anti-rollback, install/upgrade/rollback/remove tests |
| Network/auth/provider failures | GitHub error/rate/redirect tests and integrated installation outage |
| Git state failures | clean/dirty/ahead/behind/diverged/detached/conflict/worktree/forced-update fixtures |
| Harness failures | adapter lifecycle, discovery, explicit-only, evidence, rollback, and drift tests under `core/harness` |
| State/lock interruption | stale-plan, one-winner concurrency, fenced lock, append-only journal, recovery, and restart tests |
| Kill switches | strict parsing plus pre-handler and verification-journal blocking tests |

## Commands

```bash
GOTOOLCHAIN=go1.26.5 scripts/validate_assurance.sh
GDS_FULL_ASSURANCE=1 GOTOOLCHAIN=go1.26.5 \
  go test -count=10 -run '^TestFullAssuranceScenario$' ./core/assurance
tools/test-sync.sh
```

Results:

- complete `core/...` race suite: pass;
- production-size assurance report: 16/16 checks and 13/13 budgets pass;
- repeated production-size regression: 10/10 pass;
- quarantined legacy parity: 64/64 pass;
- critical forbidden external actions attempted by the assurance runner: zero.

## Defect found by the gate

The first integrated run exposed nondeterministic false schema failures under
parallel projection load. The ECMA adapter wrapped stateful `regexp2` matching
without synchronization and converted matcher errors into ordinary mismatch.
The fix serializes each matcher, retains a bounded timeout, gives projection
workers independent schema/compiler/generator ownership, and adds concurrent
regression coverage. Ten consecutive production-size runs then passed.

## Remaining proof boundary

C10 does not manufacture the exact runtime/model evidence still required by
C6. Live GitHub Apps and provider mutations (C7/C8), hosted attestations and
Linux consumer execution (C9), and managed-repository canaries/waves (C11/C12)
remain `NOT_PROVEN` or approval-gated. No result in this document authorizes
those external actions.
