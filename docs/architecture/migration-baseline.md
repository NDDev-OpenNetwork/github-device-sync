# GDS target architecture

Status: design baseline accepted for migration planning.

## Canonical design input

- Source snapshot: GDS_AGENT_SYSTEM_REDESIGN_2026-07.md
- Original source path:
  ~/Desktop/github/GDS_AGENT_SYSTEM_REDESIGN_2026-07.md
- Design baseline: 1.0.1
- SHA-256: 66d2cfb377be48b9912c5a5ee4454a808fa3f8770808ca899ae560fa325d81d4
- Phase 0 evidence: artifacts/inventory/ (removed at publication; see
  "Verification evidence" below)

The source snapshot is intentionally retained verbatim because it is the
owner-approved migration contract. Runtime instructions must not copy it
wholesale. They are compiled later into short scope-specific projections.

## Phase 0 observed architecture

The migration started from a Bash/Python estate synchronizer:

- one root Git repository;
- three direct metadata repositories tracked as submodules;
- local independent checkouts discovered under those repositories;
- nested project-owned submodules;
- human-readable status and doctor commands;
- direct clone, pull, remove, catalog, snapshot, bootstrap, and automation
  mutations;
- manually maintained instructions, skills, memories, and CI callers.

The current worktree is not a release baseline. Phase 0 recorded a large
pre-existing dirty diff and eight doctor failures. The legacy hermetic suite
still passes 64 checks.

## Target architecture

GDS becomes one agent-first control plane with strict package boundaries:

- core: portable domain, CLI, providers, compiler, reconciler, rollout, state,
  and telemetry;
- estate: estate-specific desired configuration;
- policies: reusable policy source;
- schemas: versioned contracts and migrations;
- skills: canonical gds-* skill sources, profiles, and evals;
- harnesses: versioned capability profiles and adapters;
- templates: deterministic projections;
- plugins: Codex distribution packages;
- tests: unit, contract, golden, integration, security, chaos, harness, skill,
  migration, and scale evidence.

The control plane builds an immutable bundle. Managed repositories keep a
minimal .gds/repository.yaml anchor, an exact bundle lock, and standalone
generated projections.

## Preserved working mechanisms

- Git boundary discovery and .gitmodules evidence.
- Dirty/ahead/behind/diverged classification semantics as migration fixtures.
- Fail-closed local removal guards as path-safety fixtures.
- No-force default behavior.
- The 64-check hermetic legacy suite as a parity gate.
- Full-SHA reusable workflow callers and explicit permissions.
- Device snapshot secret restrictions as input to the target device schema.
- Existing adapter/projection implementation in rldyour-ai-cli-tools as a
  separately reviewed reuse candidate.

Preservation means behavioral parity tests, not permanent retention of the
current shell architecture.

## Duplication to remove

- topology repeated in .gitmodules, shell registry, device JSON, instructions,
  skills, tests, and memories;
- manually maintained generated catalogs;
- bridge files without bundle provenance;
- skills copied or linked without a released profile contract;
- provider observations stored as desired configuration;
- harness support inferred from paths rather than runtime-tested capability
  profiles.

## Migration dependency order

1. Architecture decisions and rollback contract.
2. Schemas, stable identity, and migrations.
3. Read-only context, status, discover, inventory, validate, and doctor.
4. Policy compiler, deterministic generator, bundle lock, and golden tests.
5. Canonical skills, profiles, Codex packaging, hooks, and evals.
6. State store, journals, locks, leases, and local plan/apply/verify.
7. Module and fork workflows.
8. GitHub read-only provider.
9. Webhooks, queue, and reconciliation.
10. Separately approved GitHub mutations and rollout.
11. Cross-harness profiles, projections, and Serena memory migration.
12. Isolated harness canary execution and local legacy projection retirement.
13. Estate canary rollout, waves, and broad legacy retirement.

The current C3 implementation provides the local SQLite state boundary,
immutable plans, append-only journal, fenced locks, idempotent steps, recovery,
kill switches, and production local action handlers. C4 adds explicit session
refresh, safe checkout synchronization, unfinished-work handoff, and
dependency-ordered completion against isolated local Git providers. C5 adds
repository, module, fork, workspace, and portfolio lifecycle contracts,
including stable identity indexing and partial-failure isolation. See
`docs/contracts/state-v1.md`, `docs/contracts/operations-v1.md`,
`docs/contracts/git-workflows-v1.md`, and
`docs/contracts/lifecycles-v1.md`.

The earlier Phase 08 baseline supplied observe-only estate selectors, a
provider client, scheduler, webhook primitives, observation storage, and
bounded reconciliation fixtures. C7-C9 still require current live-provider,
controller, and rollout acceptance. No live credential or external write is
enabled; `estate/estate.yaml` keeps mutation mode disabled.

## Implementation-stack decision

ADR 0014 selects Go for the production CLI and portable core after comparing
the observed Bash/Python system with Go, Python, and Rust against the target
requirements. The decision is additive: the legacy runtime remains available
until parity, canary, and rollback gates pass.

The initial module uses Go 1.25 language compatibility and an exact Go 1.26.5
release builder. The observed local Go 1.26.4 toolchain is development-only
because the official Go vulnerability database identifies fixes in 1.26.5.
Serialization, schema, and command dependencies are isolated behind adapters
and pinned. The Python schema validator remains a temporary fixture oracle,
not a second policy authority.

ADR 0015 defines non-recursive body, full-file, and aggregate projection digest
layers. Phase 04 implements fixed policy precedence, monotonicity, per-leaf
provenance, policy distribution boundaries, in-memory standalone generation,
golden files, and fail-closed drift detection. Current legacy root projections
remain untouched until a later plan/apply migration.

## Verification evidence

The `artifacts/inventory/` Phase 0 working set below was removed when the
repository was published as public OSS. Those entries are a historical record of
what informed the migration, not live paths; the current identity and consumer
graph is compiled on demand with `gds inventory relationships`.

- artifacts/inventory/target-delta.md (removed at publication)
- artifacts/inventory/authority-conflicts.md (removed at publication)
- artifacts/inventory/secrets-and-visibility-risks.md (removed at publication)
- artifacts/inventory/not-proven.md (removed at publication)
- tools/test-sync.sh: 64/64 legacy checks pass
- Bash syntax, ShellCheck, Python syntax, snapshot validation, and Gitleaks
  passed during Phase 0
- docs/migration/phase-03-read-only-cli-evidence.md
- docs/migration/phase-03-security-review.md
- docs/migration/phase-04-policy-projection-evidence.md
- docs/migration/phase-05-skills-codex-evidence.md
- docs/migration/phase-06-state-operations-evidence.md
- docs/migration/phase-07-git-module-fork-evidence.md
- docs/migration/phase-08-provider-controller-evidence.md
- docs/migration/original-plan-acceptance-audit.md
- docs/migration/gds-completion-plan.md
- docs/migration/c0-baseline-integrity-evidence.md
- docs/adr/0018-device-workspaces-and-metadata-repository-retirement.md
- docs/migration/device-workspace-cutover-plan.md
- docs/migration/device-workspace-cutover-evidence.md
- artifacts/inventory/checkpoints/2026-07-11-c0-start/ (removed at publication)
