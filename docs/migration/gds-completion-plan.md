# GDS completion plan

Status: accepted; execution in progress

Baseline date: 2026-07-11

Normative design: `docs/architecture/GDS_AGENT_SYSTEM_REDESIGN_2026-07.md`

Observed delta: `docs/migration/original-plan-acceptance-audit.md`

## 1. Authority and scope

This file is the single authority for the remaining migration order. It does
not replace the normative architecture, runtime manifests, schemas, tests, or
phase evidence.

| Concern | Canonical owner |
|---|---|
| Architecture invariants and final acceptance | target design baseline |
| Current implementation status | acceptance audit and executable evidence |
| Remaining dependency order | this completion plan |
| Reusable runtime behavior | `core/`, `policies/`, `skills/`, `harnesses/`, `templates/` |
| Repository-owned facts | `.gds/repository.yaml` in each Git boundary |
| Observed state | controller/local state store |
| Generated context | compiler output plus bundle lock |
| Derived durable knowledge | provenance-bearing Serena memories |

No completed phase may be reopened through a second implementation path. A
missing capability extends its canonical package and CLI contract; it does not
create a parallel script, policy file, skill, or hand-maintained projection.

## 2. Status vocabulary

Only these states are used:

- `implemented-local`: code and deterministic local tests pass;
- `foundation-only`: reusable primitives exist but the end-to-end workflow is
  absent or disabled;
- `not-proven`: required runtime or external evidence was not obtained;
- `missing`: no implementation satisfies the contract;
- `conflicting`: two surfaces disagree or cross an ownership boundary;
- `blocked`: an explicit prerequisite or approval prevents safe progress;
- `accepted`: every required gate for that scope passed with durable evidence.

`implemented-local` and `foundation-only` are never synonyms for `accepted`.

## 3. Verified starting point

### 3.1. Working foundations

- Typed v1 schemas, stable identity primitives, strict serialization, local
  discovery, Git status/topology inspection, estate selectors, policy
  compilation, deterministic projection candidates, canonical skills, Codex
  package candidates, SQLite state, journals, fenced locks, generic
  plan/apply/verify, read-only GitHub provider primitives, webhook queue,
  reconciler, bundle verification, rollout planning, harness profiles, and
  memory provenance exist and pass local tests.
- The source-bound C10 runner now covers 2000 repositories, two installations,
  1000 forks, shared modules, webhook load, durable restart, isolated outage,
  compilation, projections, rollout, security/chaos suites, and 13 measured
  budgets. Its intrinsic gates pass; stage promotion still waits for the C6
  seventeen-harness runtime prerequisite.
- The legacy Bash parity suite passes 64 checks.

### 3.2. Current executable readiness after local C10 implementation

| Command | Current result | Meaning |
|---|---|---|
| `gds context` | pass | applied lock, files, compiled policy, identity, versions, and digests agree |
| `gds status` | pass | local state is classified; the accepted migration baseline is on `main` and closure changes use a bounded task branch |
| `gds validate` | pass static | runtime harness lanes remain explicitly `NOT_PROVEN` |
| `gds doctor` | `NOT_PROVEN` | all local checks pass except provisional harness runtime evidence |
| `gds generate repository --check` | pass | applied root projection equals the canonical candidate |
| `gds github doctor` | `NOT_PROVEN` | no live GitHub App evidence |
| `gds github inventory` | implemented-local | requires a private runtime and exact token permission evidence |
| `gds github governance` | implemented-local | exact inspect plus durable governance plan/apply/verify; canonical apply remains policy-disabled |
| `gds github projection-pr` | implemented-local | exact generated branch/content/draft-PR plan; canonical apply remains policy-disabled |
| `gds reconcile --plan` | implemented-local | bounded current inventory; live evidence remains unavailable |
| `gds-controller` | implemented-local | loopback service, durable queue, audit, backup, retention; not deployed |
| `gds release candidate` | blocked | `main` is inside the canary trust policy and `gds-v0.1.0` is published and attested; the remaining block is the standard `GDS_BUNDLE_SOURCE_DIRTY` clean-worktree precondition, not an out-of-policy ref or a missing artifact |
| `gds rollout plan` | pass | deterministic planning works locally |
| `gds state inspect` | pass | private schema-v5 WAL store is readable in query-only mode |

### 3.3. Current residuals

These are completion blockers, not cosmetic debt:

1. The original control-plane migration branch was published, passed hosted
   checks, and merged. Any completion follow-up still requires its own exact
   hosted checks before merge.
2. The system-default Go remains `1.26.4`, while the exact managed
   `GOTOOLCHAIN=go1.26.5` full, race, and cross-build gate passes. The hosted
   `gds-v0.1.0` release proved the same pinned builder through
   `scripts/validate_release.sh`.
3. C3-C5 local mutation, Git workflow, and lifecycle command parity is
   accepted; external GitHub mutation remains unavailable until a live C8
   runtime and its exact permission evidence are approved.
4. Six harness binaries are locally observable. All seventeen exact native runtime
   evidence records remain `NOT_PROVEN`; three binaries are absent and the
   observed Grok wrapper cannot resolve its runtime. Full acceptance remains C6.
5. C7 and C8 are implemented locally, including exact read/write capability
   separation, governance reads and writes, repository lifecycle/delete,
   custom-property and closed-ruleset handlers, generated reusable Actions
   callers, and exact projection branch/content/draft-PR publication. The
   canonical estate now permits explicitly approved operations only for managed
   NDDev source repositories. Live App access and deployment remain
   `NOT_PROVEN`; live gh-CLI permission and provider-write evidence is recorded
   per exact operation rather than inferred from this plan.
6. The quarantined Bash estate engine and independent adapter system remain
   migration boundaries until parity and rollback gates permit C12 removal.
7. The control-plane checkout and every other local Git boundary are already
   placed under the declared device workspace roots: 14 anchored boundaries,
   14 compliant, zero drift.
8. C9 now has a local reproducible release builder, independent offline trust
   verification, and a complete macOS CLI rehearsal of
   install/upgrade/rollback/remove with durable evidence. Hosted GitHub
   attestations and external artifact publication are proven: `gds-v0.1.0`
   (source commit `bace996`) was built, attested, and published on
   2026-07-24T10:11:01Z from `refs/tags/gds-v0.1.0`. Linux consumer execution,
   consumer-side verification of the published artifact on a clean device, and
   durable retention of the offline evidence directory remain `NOT_PROVEN`. The
   `example-user-ubuntu-1` device is the first concrete Linux rehearsal and supplies
   observed-but-not-accepting evidence toward closing the consumer-execution leg of
   this gap; it does not by itself accept Linux consumer execution, which still
   requires clean-device verification on a real disposable VM.
9. All 57 source records have approved reproducible content digests and all 57
   post-apply checks are unchanged. The aggregate source-freshness release gate
   remains `NOT_PROVEN` only for records whose status requires exact harness,
   GitHub App, hosted workflow, or other external runtime evidence.

## 4. Acceptance traceability

| Design acceptance area | Current state | Completion stage |
|---|---|---|
| 47.1 Architecture | local model and 14 repository anchors exist; remote managed-canary evidence does not | C0, C1, C5, C11 |
| 47.2 Source reuse | local development projection and immutable release/consumer pipeline exist; published adoption remains | C9, C11 |
| 47.3 Agent context | root projections and Codex/Claude discovery pass; full harness evidence remains | C6 |
| 47.4 Skills | static contracts and core corpus exist; runtime trigger/output/enforcement evidence is absent | C6 |
| 47.5 Git | local session, sync, handoff, completion, dependency, and cleanup contracts are accepted | C3, C4, C5 |
| 47.6 GitHub | C7 read plane is implemented locally; live App and all writes remain unproven/missing | C7, C8 |
| 47.7 Recovery | handlers, journals, locks, kill switches, restart, release rollback/remove, and integrated outage recovery pass locally | C3, C9, C10 |
| 47.8 Scale | source-bound 2000-repository gate and all 13 measured budgets pass locally | C10 |
| 47.9 Maintenance | source lifecycle, semantic review, and release gate exist; full adapter runtime evidence remains | C6, C9 |
| Security and privacy | aggregate local security/chaos gate passes; live provider and harness evidence remains | C6, C7, C8, C9 |
| Broad migration | no managed repository canary or wave was executed | C11, C12 |

## 5. Dependency graph

```text
C0 baseline integrity and reviewable commits
  -> C1 contract and maintenance closure
    -> C2 local projection cutover
      -> C3 mutation and recovery platform
        -> C4 session/handoff/complete workflows
          -> C5 repository/module/fork/workspace lifecycles
            -> C7 live GitHub read plane
              -> C8 GitHub mutation and governance
                -> C9 trusted release pipeline
                  -> C10 integrated assurance
                    -> C11 representative canary
                      -> C12 estate rollout and legacy retirement

C1 -> C6 harness adapters and evals
C2 -> C6 discovery canaries
C4 -> C6 full workflow canaries
C6 -> C10
```

No stage may consume evidence from a later stage. Independent safe work may be
developed in parallel, but acceptance follows this graph.

### 5.1. Execution ledger

| Stage | Status | Evidence |
|---|---|---|
| C0 | accepted | baseline integrity commits and current inventory checkpoint |
| C1 | accepted | deterministic contracts, source lifecycle, exceptions, validators, and verified memories |
| C2 | accepted | `docs/migration/c2-controlled-local-cutover-evidence.md` |
| C3 | accepted | `docs/migration/c3-production-mutation-recovery-evidence.md` |
| C4 | accepted | `docs/migration/c4-owner-git-workflows-evidence.md` |
| C5 | accepted | `docs/migration/c5-estate-lifecycle-evidence.md` |
| C6 | implemented-local | `docs/migration/phase-10-harness-adapters-evidence.md`; seventeen adapter lifecycles and fail-closed runtime evidence protocol exist, exact product/model runs remain `NOT_PROVEN` |
| C7 | implemented-local | `docs/migration/phase-08-provider-controller-evidence.md`; live gates `NOT_PROVEN` |
| C8 | implemented-local | `docs/migration/c8-github-governance-evidence.md`; all live writes `NOT_PROVEN` |
| C9 | implemented-local | `docs/migration/phase-09-bundle-rollout-evidence.md`; macOS lifecycle passes and hosted attestation/publication are proven by the `gds-v0.1.0` release (2026-07-24), Linux consumer execution and clean-device consumer verification remain `NOT_PROVEN` |
| C10 | implemented-local | `docs/migration/c10-integrated-assurance-evidence.md`; intrinsic gates pass, C6 prerequisite remains |
| C11-C12 | local readiness proven; external acceptance pending | `docs/migration/c11-c12-local-readiness-and-external-plan.md` and dependency-ordered sections below |

## 6. Completion stages

### C0 — Baseline integrity and reviewable commits

Objective: turn the current worktree into a reviewable, reproducible baseline
without enabling mutations.

Deliverables:

1. Re-inventory root and direct repository boundaries from current state.
   Generate a truthful observation time, source commit, worktree digest, tool
   versions, and artifact manifest. Do not rewrite historical evidence in
   place without provenance.
2. Classify every staged, unstaged, untracked, deleted, type-changed, and
   submodule change as target implementation, preserved legacy parity, user
   work, generated candidate, or accidental residue.
3. Correct the root Python test boundary so root tests cannot collect any L2/L3
   repository tests. Keep one declared root test entry point.
4. Update stale implementation docs, especially `core/README.md`, from the
   executable command surface.
5. Add hosted CI for Go format, module integrity, vet, unit/integration tests,
   schemas, memories, skills, harness registry, projections, security scans,
   and legacy parity. Preserve full-SHA action/workflow pins and least
   permissions.
6. Reverify and install the exact approved Go release builder. A source-register
   update is required if the approved version changes.
7. Split the current implementation into dependency-safe atomic commits. Work
   in each independent Git boundary first. Preserve legacy metadata changes on
   archive branches, then retire their root gitlinks under ADR 0018. Do not mix
   child preservation commits with root topology changes.
8. After source commits exist, recompute memory provenance and promote only
   memories whose sources are committed and verified.

Required gates:

- `scripts/validate_go_core.sh` passes with the exact approved builder;
- scoped Python tests, 64-check legacy suite, action lint, workflow security,
  secret scan, and `git diff --check` pass;
- hosted CI is green on the exact commit;
- root and direct repositories are clean, on declared branches, and synced;
- no unexplained generated or untracked file remains;
- memory source commits/digests match;
- Phase 0/current inventory distinction is explicit.

External approval: commit/push/PR actions and toolchain installation.

### C1 — Contract, policy, maintenance, and validation closure

Objective: close deterministic gaps before any runtime cutover.

Deliverables:

1. Add canonical schemas and migrations for policy exceptions, source-register
   records, freshness evidence, and any missing operation/result objects.
2. Implement expiring, scoped, non-weakenable policy exceptions with approval
   references and provenance.
3. Implement `gds source status`, `check`, and evidence-backed
   `mark-verified`; content changes make dependent profiles stale.
4. Complete deterministic validators for policies, context, Git state,
   security, source freshness, visibility, absolute paths, public artifacts,
   and reproducibility. Validators remain report-only.
5. Separate static acceptance from runtime evidence so a static gate can pass
   while a runtime lane remains explicitly `NOT_PROVEN`.
6. Define telemetry interfaces, stable error taxonomy, redaction, and report
   schemas before deploying a controller.
7. Resolve all source-of-truth conflicts identified by the refreshed inventory;
   every mutable fact receives one canonical owner.
8. Complete the `gds memory` read/generate/validate contract: staleness,
   provenance, visibility, source references, and candidate output remain
   deterministic and never silently rewrite tracked knowledge.

Required gates:

- all canonical YAML/JSON uses strict schemas and deterministic serialization;
- policy provenance covers every effective leaf;
- weakening without a valid exception is rejected;
- stale source facts block affected adapter/release lanes;
- public/private and secret fixtures fail closed;
- static validation exits success with zero hidden `NOT_PROVEN` conversion.

External approval: none for local implementation.

### C2 — Controlled local projection cutover

Objective: make the control-plane repository itself a valid managed GDS
repository before managing anything else.

Deliverables:

1. Add a local plan/apply/verify materialization handler for exact generated
   repository files. It must preserve unexpected drift and support rollback.
2. Materialize the root bundle lock, compiled policy, concise `AGENTS.md`, and
   first-class `.claude/CLAUDE.md` from committed canonical inputs.
3. Retire mechanical root instruction imports/symlinks only after discovery
   parity. Do not edit generated output directly.
4. Initialize the private XDG state database through an explicit local plan.
5. Restart Serena/Codex and prove Go, Python, and Bash semantic discovery from
   the tracked project configuration.
6. Run an isolated read-only discovery canary for every locally available
   harness without changing the user's active global configuration.

Required gates:

- `gds context`, `status`, projection validation, and local state inspection
  succeed;
- two consecutive generations are byte-identical;
- root and nested instruction sources, order, byte count, and digests are
  recorded;
- duplicate instruction and skill count is zero;
- rollback restores the prior local projection set exactly;
- no public/private boundary is crossed.

External approval: local harness/plugin installation if an isolated home still
changes device state.

### C3 — Production mutation and recovery platform

Objective: connect the proven operation engine to bounded real handlers without
yet enabling GitHub writes.

Deliverables:

1. Expose a uniform CLI transaction grammar: side-effect-free plan, exact
   apply, explicit verify, operation inspection, and recovery planning.
2. Implement concrete local filesystem and Git precondition checkers and action
   handlers using argv execution, cancellation, output caps, path confinement,
   redaction, and stable exit classes.
3. Add state initialization/migration, operation reports, lock recovery, durable
   cursors, idempotency keys, compensation planning, and safe restart behavior.
4. Implement and surface all four kill switches. Every operation report records
   their effective state.
5. Make approval evidence scope-bound and non-secret; approval cannot expand a
   stored plan.

Required gates:

- mutation without plan or approval succeeds zero times;
- expired/tampered/stale plans and changed OIDs/policies/manifests block before
  handlers;
- concurrent apply has one winner;
- interruption at every step is resumable or produces an explicit recovery
  plan;
- path traversal, symlink races, shell injection, and secret logging tests pass;
- journal and before/after evidence remain append-only.

External approval: none for fixture/local sandbox implementation.

### C4 — Session, synchronization, handoff, and completion workflows

Objective: implement the three owner-facing Git workflows end to end.

Deliverables:

1. `gds session start`: resolve scope, classify all relevant Git boundaries,
   optionally perform policy-approved non-integrating refresh, and never merge,
   checkout, fast-forward, publish, clone, or clean.
2. `gds sync checkouts`: update only explicitly selected clean boundaries under
   a plan; preserve dirty, diverged, detached, no-upstream, and forced-update
   states.
3. `gds handoff`: plan the exact file set, tests, branch, remote OID, and draft
   PR policy; apply commit/push/PR only after exact approval; never merge or
   clean.
4. `gds complete`: resolve the affected repository graph, finalize dependencies
   first, update eligible pins, verify consumers, integrate under repository
   policy, publish, then clean only reachability-proven branches/worktrees.
5. Store structured handoff/completion reports and exact next-session context.

Required gates:

- real temporary Git/bare-remote fixtures cover clean, dirty, ahead, behind,
  diverged, detached, conflict, force-update, no-upstream, and multiple
  worktrees;
- handoff never stages unapproved untracked files;
- completion never leaves consumer main on a temporary dependency pin;
- cleanup never removes unique/unpublished commits or active worktrees;
- every external-looking action is tested first through an isolated provider
  double; live GitHub apply remains disabled until C8.

External approval: any real commit, push, PR, integration, or cleanup.

### C5 — Repository, module, fork, workspace, and portfolio lifecycles

Objective: complete the target domain command surface on top of C3/C4.

Deliverables:

1. Repository commands for onboard, rename, transfer, archive, materialize,
   remove-checkout, and separately gated delete.
2. Module commands for add, update-pin, remove, release, and selected consumer
   updates; model consumption, pinning, and publication independently.
3. Fork inspect/sync/detach/archive workflows that preserve maintained commits
   and never force by default.
4. Workspace/device planning and selected checkout materialization with bounded
   cloning, worktrees, partial-clone policy, and path safety.
5. Portfolio-wide change planning as one aggregate plan plus independent
   repository subplans.
6. A stable identity/relationship index and consumer graph. Repository owner,
   path, and provider name remain locators, not identity.

Required gates:

- rename/transfer preserve stable IDs and relationship integrity;
- module pin publication and final-ref reachability are proven;
- shared-consumer and mixed consumption fixtures pass;
- force paths require exact old OID, recovery ref, approval, and verification;
- deletion remains a separate explicit workflow;
- one repository failure is isolated and visible in aggregate plans.

External approval: real repository, module, fork, checkout, or portfolio writes.

### C6 — Harness adapter lifecycle and behavioral evaluation

Objective: make all seventeen canonical harness profiles operational from one source,
without manual content forks.

Deliverables:

1. One adapter interface implementing detect, inspect, plan-install, apply,
   verify, render instructions/skills/hooks, update, rollback, remove, and
   doctor.
2. Reverified official-source evidence and exact capability profiles for
   Claude Code, Codex, OpenCode, Pi, ZCode, MiMo Code, Kimi Code,
   Antigravity CLI, Cursor CLI, and Grok CLI.
3. A runtime harness/eval runner that stores exact product version, model label,
   tools, OS, architecture, profile digest, transcripts, assertions, and result
   digest.
4. Discovery, explicit invocation, trigger, output, and enforcement execution;
   the existing corpus becomes executable evidence rather than static input.
5. Complete eval profiles beyond the core profile. Critical destructive paths
   remain explicit-only on every harness.
6. Migrate or retire the separate legacy adapter control plane only after GDS
   parity. Reuse code through one canonical owner; never copy competing skills
   or policies.
7. Model Antigravity CLI only through its reverified current native contract.
   Remove the predecessor harness identity and legacy root-instruction
   projection from owned adapter sources, generated outputs, tests, validators,
   memories, and operator documentation after replacement evidence passes.

Required gates per harness:

- all twelve runtime-contract cases pass for an exact version/model profile;
- exact discovered instruction and skill sets match, with zero duplicates;
- explicit invocation pass rate is 100%;
- positive trigger recall and near-miss specificity meet the design thresholds;
- critical forbidden mutation success count is zero;
- update, rollback, and remove preserve user-owned state;
- public/private fixture passes.
- the active owned adapter system contains no predecessor harness identity or
  legacy root-instruction projection, and no compatibility alias can re-enable
  one.

Profiles remain `provisional` independently until their own gates pass. Final
system acceptance requires all seventeen target profiles accepted.

External approval: installing, trusting, updating, or removing real harness
configuration and hooks.

### C7 — Live read-only GitHub control plane

Objective: deploy current GitHub observation without granting mutation power.

Prerequisite decisions, recorded in ADRs before deployment:

- controller hosting and availability model;
- SQLite single-controller versus PostgreSQL/queue transition threshold;
- data retention and backup policy;
- GitHub plan capabilities and App installation scope;
- secret-manager adapters for macOS, Linux, server, and CI.

Deliverables:

1. Concrete secure token/key adapters and an Inventory App with minimum read
   permissions.
2. Controller service entry point, durable queue, health endpoints, metrics,
   structured logs, redaction, backups, and recovery runbook.
3. HMAC webhook ingress, delivery monitoring, dead-letter handling, targeted
   reconciliation, and scheduled full installation reconciliation.
4. Current provider inventory, access states, settings/actions/ruleset drift,
   request/rate telemetry, and signed audit snapshots without secrets.
5. Expose read-only `gds reconcile --plan` and scoped `gds report` surfaces;
   provider apply remains unavailable until C8.

Required gates:

- effective permissions equal or are narrower than declared permissions;
- inaccessible/auth-failed/not-found states remain distinct;
- webhook acknowledgement, deduplication, replay, ordering, retry, and outage
  recovery pass;
- full read reconciliation completes with bounded API/network resources;
- no mutation credential is available to the read service;
- provider evidence is current and traceable by request ID.

External approval: GitHub App creation/installation, credentials, endpoint, and
controller deployment.

### C8 — GitHub mutations, governance, and reusable Actions

Objective: add least-privilege provider writes behind C3 plan/apply/verify.

Deliverables:

1. A separately scoped Mutation App and handlers for branches/content, PRs,
   repository lifecycle, selected settings, rulesets/protection, custom
   properties, and workflow callers.
2. Expected-old-state checks, idempotency, request journaling, rate-aware
   mutation spacing, compensation plans, and zero blind retries.
3. A generated thin-caller contract for the existing reusable workflow
   authority or a separately approved public-safe workflow distribution. Do
   not duplicate workflow logic.
4. Governance drift reports for organization and personal repositories with
   `managed`, `observed`, and `ignored` ownership per field.
5. Stable required-check names, full-SHA action/workflow pins, explicit
   permissions, and OIDC policy where applicable.

Required gates:

- Mutation App cannot read or mutate repositories outside approved scope;
- read-only service cannot mint mutation credentials;
- stale provider state blocks apply;
- pull-request and settings fixtures prove idempotency and safe compensation;
- workflow security, permissions, untrusted input, provenance, and secret tests
  pass;
- broad bypass, auto-merge, force, visibility, delete, and permission changes
  remain separately gated.

External approval: App permissions/installations and every live provider write.

### C9 — Trusted bundle, CLI, plugin, and release pipeline

Objective: produce the first installable immutable GDS release.

Deliverables:

1. Reproducible release builds using the exact approved builder and locked
   dependencies for macOS/Linux target architectures.
2. Immutable bundle/CLI/plugin artifacts with manifest, checksums, monotonic
   release sequence, build-provenance attestation, SBOM, changelog, migration
   notes, and offline verification material.
3. Consumer verification of artifact digest, owner, repository, workflow, ref,
   source commit, sequence floor, and trust root before installation.
4. Canary/stable/frozen channels that resolve to immutable exact artifacts, not
   mutable runtime imports.
5. Exact approved rollback authorization and a corrective-release workflow with
   a new higher sequence.

Required gates:

- independent rebuilds are byte-identical;
- private estate data and secrets are absent from portable artifacts;
- tampered artifact, attestation, identity, workflow, ref, sequence, SBOM, and
  offline evidence are rejected;
- install, verify, upgrade, rollback, and remove pass in clean macOS/Linux
  fixtures;
- source freshness and all required harness/skill/security gates block release
  when stale or incomplete.

External approval: tag, release, artifact publication, and trust-root changes.

### C10 — Integrated security, chaos, scale, and performance acceptance

Objective: prove the complete system under realistic failure and estate scale
before touching broad repository sets.

Deliverables:

1. Integrated simulation of 2000 repositories, two installations, 1000 forks,
   shared modules, mixed lifecycles/access states, webhook load, reconciliation,
   compilation, rollout, and worker restart.
2. Recorded budgets for context latency, repository status, inventory,
   reconciliation, generation, API calls, queue lag, memory, DB size, and
   rollout throughput.
3. Security suites for prompt injection, secrets, public/private leaks,
   malicious names/paths, symlink traversal, command injection, untrusted
   forks, webhook replay, token scope, workflow supply chain, and artifact
   poisoning.
4. Chaos/recovery suites covering network/auth/provider/Git/harness/state/lock
   failures from section 41.9 of the design.
5. Migration and rollback rehearsals from the last legacy baseline and previous
   immutable GDS release.

Required gates:

- bounded resources and durable cursors survive restart;
- one repository/installation failure does not block unrelated targets;
- no critical forbidden action succeeds;
- every performance budget has measured evidence;
- no unbounded process, API, Git, or filesystem fan-out exists;
- rollback and kill switches are exercised, not merely documented.

External approval: none; use fixtures/test accounts only, never production mass
mutations.

### C11 — Representative managed-repository canary

Objective: prove real repository adoption and real agent sessions with minimal
blast radius.

Canary selection must include:

- private and public repositories;
- personal and organization ownership;
- project, module, fork, and submodule-consumer roles;
- nested instruction scope and multiple-worktree cases;
- representative stacks;
- observe-only/archived negative cases.

Deliverables:

1. Exact canary plan and approved target IDs.
2. Stable anchors, compiled policies, bundle locks, standalone projections,
   thin workflow callers, and selected skill profiles through one PR per Git
   boundary.
3. Real clean sessions across applicable harnesses from root, nested,
   standalone module, embedded module, and worktree contexts.
4. Live drift, checks, review, merge, reconciliation, rollback, and recovery
   evidence.

Required gates:

- every canary satisfies repository onboarding DoD;
- zero critical drift, private leak, duplicate context, or forbidden mutation;
- required CI/check/review evidence is current;
- rollback to the prior immutable bundle is proven on a canary;
- no next ring starts automatically after any failure.

External approval: branches, PRs, merges, canary release, settings, and rollback.

### C12 — Estate waves, final reconciliation, and legacy retirement

Objective: migrate the selected managed estate and remove superseded authority
only after proven parity.

Deliverables:

1. Wave rollout through deterministic rings with bounded concurrency,
   per-repository subplans, pause gates, durable cursors, and aggregate reports.
2. Repository-local anchors and projections for every managed target; offline
   and observe-only targets remain explicit, not falsely compliant.
3. Final reconciliation of provider, desired, local, bundle, projection,
   workflow, harness, memory, module-pin, and security state.
4. Controlled retirement of legacy shell mutators, global pointer generation,
   copied/symlinked policy bridges, numeric legacy memories, committed provider
   catalogs, obsolete skills/hooks, and competing adapter authorities.
5. Preserve useful local portfolio navigation, but remove its role as machine
   identity or policy inheritance.
6. Final acceptance report, operational runbooks, retention/backup evidence,
   source-review schedule, and incident ownership.

Required gates:

- all section 47 acceptance criteria pass with durable evidence;
- all managed repositories have zero critical drift;
- broad rollout is resumable and completed within measured budgets;
- no required workflow depends on the legacy runtime;
- legacy restore evidence remains available for its retention window;
- source freshness, harness compatibility, and periodic reconciliation are
  scheduled and observable.

External approval: every rollout wave, merge policy, settings change, cleanup,
archive, and final legacy deletion.

## 7. Synchronization protocol for every stage

Every stage uses this order:

1. Resolve current scope, Git boundaries, applicable policies, source freshness,
   and dirty/untracked state.
2. Update the canonical owner only.
3. Add or update deterministic tests before enabling a mutation path.
4. Generate candidates; never edit generated files directly.
5. Review semantic and security diffs, including public/private flow.
6. Run the smallest applicable gate, then the complete stage gate.
7. Update contracts, ADRs, runbooks, changelog, and source register only when
   verified facts changed.
8. Regenerate Serena memories from committed sources and validate provenance.
9. Commit independently in each Git boundary. For dependency changes, commit
   and publish the dependency first, then update the consumer pin/gitlink.
10. After explicit approval, push, verify remote OIDs and hosted checks, and
    journal external results.
11. Require a clean verified boundary before advancing the dependent stage.

Documentation never claims a later stage. Evidence records the actual command,
version, environment, result, failures, and unproven checks.

## 8. Atomic commit strategy for the current worktree

The exact split is finalized by the C0 change ledger, but it must preserve this
dependency order:

1. architecture/ADRs/schemas/migrations;
2. identity/serialization/read-only domain and CLI;
3. policy compiler/projections/golden tests;
4. skills/plugins/harness static contracts and eval inputs;
5. state/operations;
6. Git/module/fork read-only adapters;
7. GitHub/webhook/reconciler foundations;
8. bundle/rollout foundations;
9. CI, runbooks, source register, evidence, and verified memories;
10. direct metadata repository commits, followed by root gitlink updates.

Each commit must compile and pass its applicable tests. If dependencies make a
smaller split non-buildable, combine only the minimal dependency closure and
record the reason.

## 9. Approval checkpoints

| Checkpoint | Required before |
|---|---|
| A1 | system toolchain or real harness installation |
| A2 | commit/push/PR of the stabilized current worktree |
| A3 | GitHub App creation, installation, permission, secret, or endpoint changes |
| A4 | enabling any real GitHub mutation handler |
| A5 | tag, release, artifact, attestation, SBOM, or public distribution |
| A6 | canary repository branches, PRs, merges, settings, or rollback |
| A7 | each estate rollout wave |
| A8 | archive, deletion, permission broadening, force operation, or final legacy retirement |

Approval is exact to plan ID, object IDs, action set, and expiry. One approval
cannot silently authorize later checkpoints.

## 10. Final completion condition

GDS is complete only when:

- the entire target CLI and validator surface is implemented and documented;
- every mutating path uses plan/apply/verify, stale-state rejection, journal,
  idempotency, and recovery;
- all seventeen harnesses pass exact runtime contracts and behavioral evals;
- GitHub read/write identities are least-privilege and operationally separated;
- immutable release provenance and rollback are proven;
- integrated security, chaos, and 2000-repository scale gates pass;
- representative canaries and all approved waves reconcile cleanly;
- no competing reusable source or manual generated projection remains;
- operational docs, source freshness, memories, CI, local state, provider state,
  and repository locks all identify the same accepted release and policy
  digests;
- legacy removal is complete and its recovery evidence is retained.

Stopping after a local foundation, a static profile, a generated candidate, or
a successful unit test does not satisfy completion.
