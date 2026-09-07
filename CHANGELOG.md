# Changelog

All notable changes are documented here. The project follows Semantic
Versioning.

## [Unreleased]

- Publish unsuccessful completed self-workflow attempts as unassigned,
  repository-local CI evidence; preserve actual conclusions and exact attempts.


- Reject ineligible module pins and invalid consumer policy before running
  module verification commands; eligible plans and apply still verify the exact
  published target and bind that evidence to the transaction.
- Add opt-in `continuous-development` policy with explicit, journaled removal
  intent for selected repository ruleset status checks and generated advisory CI
  guidance. Preserve omitted rules and unknown status-check parameters across
  updates; verify empty ruleset readback and replay without another write.

### Changed

- Generated Go callers honor declared fast verification commands, falling back
  to the existing test command when none are declared. GDS uses a dedicated
  formatting/module/vet/schema fast mode and runs its full Go tests once in the
  PR-required job; the complete build remains in that job.

- Ruleset reconciliation compares the complete owned required-check identities,
  including integration IDs and strict policy. Retired extra contexts or a
  different check producer no longer appear synchronized; external parameters
  and unowned rule types remain preserved.

- Drakkars audit, triage, orientation and rollout skills now model the current
  OTEL/OTLP/OpenObserve boundary, classified host-signal metrics, host
  compliance coverage, external backend heartbeat and bounded alert-silence
  recovery semantics.
- Correlation guidance preserves raw sparse identity during queued/assigned
  capacity phases and treats only a running intent beyond its state-entry grace
  as a persistent correlation fault.
- Workflow audit guidance no longer assumes an Actions-read `GITHUB_TOKEN` can
  read the repository retention Administration endpoint.

## [0.1.2] - 2026-08-24

### Changed

- CI and Drakkars skills now qualify changes only with naturally occurring
  project jobs. They forbid synthetic, benchmark, soak, canary, manual rerun,
  empty-commit and cancellation traffic created solely for rollout evidence.
- Fleet audit guidance now verifies the contended 75-percent per-repository
  envelope, uncontended full-fleet use, private-free runner routing, and both
  queued and already-running repository correlation.
- Explicit-only skills now use the native `agents/openai.yaml` invocation
  policy as their canonical Codex control. The catalog no longer requires the
  unsupported legacy `disable-model-invocation` SKILL frontmatter key; portable
  adapters continue to derive invocation behavior from the typed registry.
- Release and platform workflows now run entirely on GitHub-hosted public
  capacity and cover Linux amd64/arm64 plus macOS arm64 without exposing private
  estate runner labels.
- Public module coverage can compare declared required contexts with live
  provider enforcement without requiring the private estate to duplicate a
  second version ledger.

### Fixed

- Public anchors no longer inherit the archived private control plane's schema,
  repository identity or embedded projection templates.
- Static analysis now covers the Go engine and Actions workflows on the public
  authority before a private estate can advance its gitlink.
- The release workflow now installs and uses one job-scoped hash-locked Python
  environment instead of depending on undeclared runner packages or a second
  tool-managed interpreter.
- The declared `gds-drakkars` plugin now has its canonical source manifest, so
  full and release validation cannot silently stop at a redirected JSON result.
- Release sequence is an explicit monotonic dispatch input rather than the
  repository-local workflow run number, preserving the accepted sequence ledger
  across the public-authority migration.
- Release lifecycle operations invoked from a private control plane now bind
  their committed implementation proof to its pinned public GDS engine root.
- Device bootstrap verifies the physical seed Go binary with
  `GOTOOLCHAIN=local`, so automatic toolchain selection cannot make an already
  installed pinned toolchain appear stale on every run.

## [0.1.1] - 2026-08-16

First release of `github-device-sync` (GDS) as an open-source control plane
under `github.com/NDDev-OpenNetwork/github-device-sync`. The version line starts
here: this is a new module path, so the numbering of the predecessor it grew out
of does not carry over and would only resolve to releases no tag here can name.

### Added

- **Canonical estate**: a declarative description of owners, installations,
  selectors, portfolios and devices under `estate/`, loaded and validated as one
  tree so an inconsistent estate is refused before anything is planned. The
  repository ships an example estate; a real one is passed in with `--cwd` and is
  never vendored here.
- **Policy compiler**: deterministic composition of base, owner, portfolio, role,
  stack, lifecycle and repository tiers into one compiled policy per repository,
  with the source of every effective value recorded.
- **Projections**: per-repository artefacts rendered from that compiled policy —
  agent instructions, harness adapters and GitHub Actions workflows — as content
  addressed candidates that can be generated, compared and verified without
  being applied.
- **Read-only GitHub inventory and reconciliation**: compile what a GitHub
  installation actually contains and compare it with declared intent, without
  loading mutation credentials.
- **Operations**: durable, journaled plans with explicit steps, locks and
  recovery, so an interrupted change can be inspected and resumed rather than
  guessed at.
- **`gds` CLI** over all of the above, emitting one typed envelope per command
  so results are machine-readable without parsing prose.

### Notes

GDS describes and reconciles repository estate; it is not a CI system, a secret
store or a deployment tool. Provider mutation is deliberately separated from
observation: inventory and comparison never hold write credentials.
