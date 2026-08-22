# Changelog

All notable changes are documented here. The project follows Semantic
Versioning.

## [Unreleased]

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
