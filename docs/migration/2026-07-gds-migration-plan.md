# GDS migration plan

Status: active plan, no external mutation authorized.

Remaining execution is canonical in
`docs/migration/gds-completion-plan.md`. This file preserves the historical
phase record and must not grow a competing completion sequence.

## Update 2026-07-24 — external release and attestation proven

The phase gate entries below are the historical record as written at each
phase's completion and are left unchanged. One of their standing `NOT_PROVEN`
claims has since been discharged and must be read against this note:

- The Phase 9 entry states that "External release, attestation, repository PRs,
  and live rollout remain `NOT_PROVEN` and disabled". External release and
  attestation are now proven. `gds-v0.1.0` (source commit `bace996`) was built,
  attested, and published on 2026-07-24T10:11:01Z from `refs/tags/gds-v0.1.0`
  by `.github/workflows/release-bundle.yml`, with keyless Sigstore SLSA build
  provenance and an SBOM attestation. The repository is public and owned by the
  example-org organization, so artifact attestation is an available path.
- The rest of that entry stands: live rollout remains `NOT_PROVEN` and
  disabled, as do the Phase 8, 10, and 11 `NOT_PROVEN` claims. Migration rule 6
  is unchanged — nothing here authorizes a further external mutation without an
  approved plan naming the exact action.

## Objective

Replace the legacy estate synchronizer with the GDS control plane without losing
working behavior, private data, Git history, or recovery evidence.

## Baseline

- Control-plane HEAD: 433c46b6923f7dc1efb96713b9ffc9330ca8ba58
- Remote main matched the local HEAD during Phase 0.
- The worktree contains pre-existing staged, unstaged, untracked, and submodule
  changes.
- No Phase 0 artifact changed the Git index or any remote.
- Required evidence lives in artifacts/inventory/.

## Migration rules

1. Add target paths before replacing legacy paths.
2. Keep legacy behavior runnable until parity gates pass.
3. Never combine terminology, schema, CLI, harness, Git workflow, and rollout
   changes in one concern.
4. Treat every independent repository as a separate mutation boundary.
5. Keep external writes disabled until an approved plan names the exact action.
6. Do not install a GitHub App, change repository settings, open mass PRs, push,
   merge, release, or delete during local implementation phases.
7. Preserve unrelated dirty work.
8. Record unavailable evidence as NOT_PROVEN.

## Phase gates

### Phase 1: decisions

Deliver:

- architecture index;
- accepted ADR set;
- rollback runbook;
- target specification snapshot.

Gate:

- all ADRs have Context, Decision, Consequences, Alternatives, Verification, and
  Rollback;
- links and design digest validate;
- no legacy runtime file changes.

### Phase 2: schemas and identity

Deliver:

- schemas/v1/estate.schema.json;
- schemas/v1/repository.schema.json;
- schemas/v1/policy.schema.json;
- schemas/v1/harness-profile.schema.json;
- schemas/v1/device.schema.json;
- schemas/v1/plan.schema.json;
- schemas/v1/operation-result.schema.json;
- schemas/migrations/registry.yaml;
- .gds/repository.yaml for the control plane;
- schema fixtures and validators.

Gate:

- JSON Schema 2020-12 validation;
- closed objects and explicit enums;
- stable identity round-trip;
- invalid fixtures fail for the expected reason;
- no runtime switch.

### Phase 3: read-only CLI

Deliver:

- gds context;
- gds status;
- gds discover;
- gds inventory;
- gds validate;
- gds doctor;
- versioned JSON envelopes and exit classes.

Gate:

- read-only sandbox tests;
- real Git fixtures;
- no external writes;
- parity comparison with legacy observations.

### Phase 4: compiler and projections

Deliver:

- policy precedence and provenance;
- deterministic AGENTS and harness projections;
- bundle lock;
- golden and reproducibility tests;
- manual-drift detection.

Gate:

- repeated generation is byte-identical;
- public/private fixtures pass;
- legacy projection behavior is either preserved or intentionally superseded by
  ADR.

### Later phases

Proceed in the dependency order documented in docs/architecture/README.md. Each
phase requires its own test evidence and rollback path.

## Legacy quarantine

Do not expose these current operations through a new default workflow until
replacement parity exists:

- automatic metadata commit/push;
- automatic root gitlink bump/push;
- pull-time fast-forward;
- direct rm -rf checkout removal;
- age-only stale-lock deletion;
- global harness file mutation;
- predictable temporary provider inventory files.

## Commit plan

When the owner later authorizes commits, keep concerns independent:

1. docs: architecture baseline and ADRs;
2. schema: v1 contracts and fixtures;
3. core: identity and relationships;
4. cli: read-only commands;
5. policy and generation;
6. skills and Codex packaging;
7. state and operations;
8. provider/controller;
9. memory migration;
10. legacy retirement.

No commit or push is authorized by this plan alone.

## Current progress

The complete acceptance delta is maintained in
`docs/migration/original-plan-acceptance-audit.md`. The redesign is not yet
production-complete.

- Phase 0 read-only inventory: completed; evidence is in
  `artifacts/inventory/`.
- Phase 1 architecture decisions: completed locally; accepted ADRs and rollback
  runbook are present.
- Phase 2 schemas and identity: completed locally; evidence is in
  `docs/migration/phase-02-schema-identity-evidence.md`.
- Phase 3 read-only CLI: completed locally for development; evidence is in
  `docs/migration/phase-03-read-only-cli-evidence.md`. Release evidence remains
  blocked until the trusted Go builder is upgraded from 1.26.4 to the pinned
  1.26.5 security release. No legacy runtime switch is authorized.
- Phase 4 policy compiler and projections: completed locally as an in-memory
  candidate and read-only drift gate; evidence is in
  `docs/migration/phase-04-policy-projection-evidence.md`. Existing root
  projections remain untouched and no development lock is installed.
- Phase 5 canonical skills, profiles, Codex packaging, hooks, and eval inputs:
  completed locally for static contracts; evidence is in
  `docs/migration/phase-05-skills-codex-evidence.md`. Runtime discovery and
  model-dependent evaluations remain explicitly `NOT_PROVEN`; no plugin or hook
  was installed or trusted.
- Phase 6 local state, journals, locks, leases, and the plan/apply/verify core:
  completed locally; evidence is in
  `docs/migration/phase-06-state-operations-evidence.md`. Only read-only plan
  and state inspection is exposed through the CLI. Production action handlers,
  recovery, and external mutations remain disabled.
- Phase 7 local Git, module, and fork read-only foundations: completed locally;
  evidence is in `docs/migration/phase-07-git-module-fork-evidence.md`.
  Network refresh, integration, pin updates, force operations, and production
  handlers remain disabled.
- Phase 8 GitHub read-only provider, scheduler, webhook queue, and
  reconciliation: completed as an isolated local foundation; evidence is in
  `docs/migration/phase-08-provider-controller-evidence.md`. Live credentials,
  App permissions, webhook deployment, and provider access remain
  `NOT_PROVEN`.
- Phase 9 immutable bundle trust, anti-rollback state, and canary/wave rollout
  controls: completed locally; evidence is in
  `docs/migration/phase-09-bundle-rollout-evidence.md`. External release,
  attestation, repository PRs, and live rollout remain `NOT_PROVEN` and
  disabled.
- Phase 10 cross-harness registry, static adapters, canonical explicit-only
  metadata, Claude first-class projection, and bounded runtime detection:
  completed locally; evidence is in
  `docs/migration/phase-10-harness-adapters-evidence.md`. Clean isolated
  instruction/skill/hook runtime suites remain `NOT_PROVEN`, so every profile
  is still provisional.
- Phase 11 Serena provenance migration and the clean harness canary contract:
  completed locally; evidence is in
  `docs/migration/phase-11-memory-canary-evidence.md`. The active Serena process
  must restart before Go LSP discovery is proven, and interactive harness
  canary execution remains `NOT_PROVEN`.
- Phase 12 isolated harness canary execution and controlled legacy projection
  retirement: next.
