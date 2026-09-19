# Changelog

All notable changes are documented here. The project follows Semantic
Versioning.

## [Unreleased]

- Add `gds evidence record` and `gds evidence verify`: repo-scoped session
  evidence for the repository the agent ran in, including every Git module
  inside its boundary. `record` binds the `.gds/repository.yaml` identity,
  HEAD/upstream position, change counts and changed paths, and per-submodule
  gitlink/checked-out OIDs into a canonical artifact signed with Ed25519 under
  the `gds-session-evidence/v1` domain (`session-evidence` trust role), links
  it to the previous artifact for the same repository, and writes it under the
  device state root with mode `0600` — never into the repository. `verify`
  checks the embedded schema, canonical digest, signature and structural
  invariants without the recording session, harness or repository. New schema
  `session-evidence` with valid and invalid fixtures.
- Remove release channels from the release pipeline (ADR 0038). Bundle
  manifests, release envelopes, locks, rollout documents and trust policy no
  longer carry or require `channel`; the fields remain optional for decoding
  documents produced while the field existed, and a legacy channel is still
  checked against a consumer policy that lists it.
- Classify bundles by `release_sequence` alone: `0` is a development
  projection, `>= 1` is a release. Development locks no longer record a
  `development` channel.
- Require every release build to come from the exact
  `refs/tags/gds-v<version>` ref. The release workflow now runs on pushes to
  `main`: a privileged resolve job derives the next patch version and
  monotonic sequence from the latest published envelope over the GitHub API
  without checking out candidate source, creates the tag, and hands the exact
  identity to the unprivileged build job.
- Drop harness evidence from the release builder's inputs and from
  `bundle.Build` gating. Harness evidence stays a separately produced and
  verified estate/runtime signal in `core/harnessevidence` and the module
  release evidence path; it no longer decides whether an artifact may be
  published.
- Remove `default_bundle_channel` from the estate schema and the example
  estate, and remove the `--channel` flag and evidence inputs from
  `gds-release-builder` and the release-candidate command.

## [0.9.7] - 2026-09-19

- Skip hidden directories during workspace discovery so tool-state and
  fixture trees such as `.tmp/` no longer manufacture anchor findings;
  `.git` stays the discoverable boundary.
- Observe quarantine remotes read-only through the fetch URL so checkout
  quarantine plans work for SSH/HTTPS remotes while network pushes remain
  gated behind `validatedPushURL`.

## [0.9.6] - 2026-09-16

- Cut 0.9.6 as the next published bundle after `gds-v0.9.5`, which remains a
  source tag whose GitHub Release has no bundle assets.
- Keep the 0.9.5 `desktop-server` class and `gds context` `context.device`
  binding.

## [0.9.5] - 2026-09-15

- Surface the registered device and its `class` (`profile`/`gui`/`docker_mode`/
  `execution_policy`) on `gds context` from the device-local locator bound to
  exactly one `estate/devices/*.yaml`. `gds-orient` reads `context.device`
  instead of inferring the host.
- Add the `desktop-server` device class: Linux x86_64, GUI required, Docker
  none|rootful|rootless (default none). The bootstrap orchestrator forwards
  `docker_mode` and server-baseline hardening flags. Name it in `gds-orient`
  and `gds-bootstrap-device` the same way the schema already does.
- Keep `append_hardening_flags` `set -e` safe when ssh/ufw/fail2ban are unset,
  so desktop-server apply does not exit before phase 0.
- Allow identical shared harness projections and dot-prefixed repository names.
- Strip registration planning inputs from operations payloads.
- Bound native integration suites inside the platform job budget and wait for
  escaped-child startup before cancellation.
- Record extra-approval false on `main` and pin reusable workflows to current
  module mains.

## [0.9.4] - 2026-09-13

- Validate release evidence before the expensive build gates.
- Run the full race suite once per release gate.

## [0.9.3] - 2026-09-13

- Make device onboarding consistent and refresh the release toolchain.
- Validate formatting with the selected Go toolchain.
- Cover Intel macOS and consume the reviewed workflow library.
- Refresh generated fixtures and portable Python dependency locks.

## [0.9.2] - 2026-09-12

- Add a public, exact-attempt harness evidence producer that rejects private
  source identities, stale/foreign/duplicate jobs and unsupported signer
  authority. Preserve independent key validity and bind expiry to real runtime
  age instead of renewing old CI evidence at packaging time.
- Repair capability-aware governance, managed-to-observed policy overrides,
  public source-register ownership and consuming-estate bootstrap boundaries.
  Provider-only discovery no longer invents missing-anchor findings.

- Honor explicit estate selection for governance policy compilation and local
  comparison, retaining authority checks and the same root during apply.

- Update workflow dependencies through the canonical Go caller anchor and
  regenerated provenance. Exclude that generated dependency from direct
  Dependabot rewrites while retaining other workflow update proposals.

- Select continuous development for the GDS repository itself and document
  explicit ruleset removal plus cautious readback after ambiguous write errors.
  Generic policy defaults remain opt-in for other consumers.

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
