# Phase 09 bundle trust and rollout evidence

Status: trusted local release/consumer foundation and macOS lifecycle
rehearsal implemented; hosted attestation, Linux consumer rehearsal, canary,
and external rollout NOT_PROVEN.

Date: 2026-07-11

## Completed

- Added strict bundle manifest, release envelope, trust, installation,
  rollback-authorization, rollout request, rollout plan, and plan-parameter
  schemas with positive/negative fixtures.
- Added stable SemVer parsing and comparison shared by builder and consumer.
- Added a clean-source release builder pinned to Go 1.26.5. It builds `gds`,
  `gds-controller`, and `gds-codex-runtime-driver` twice for four supported platforms, packages all three
  Codex plugins, creates an SPDX 2.3 SBOM, assembles twice, and writes one exact
  six-file release directory atomically.
- Added source-commit and exact source-ref binding. Stable/frozen releases
  require the exact version tag; canary requires main or that tag.
- Added a manual SHA-pinned GitHub Actions workflow with minimal permissions,
  serialized execution, provenance for five subjects, SPDX attestation for the
  artifact, offline bundles, and no tag/release/rollout mutation.
- Added independent local `trusted-root.jsonl` digest pinning. Producer CI and
  consumer verification both fail when the current offline root differs.
- Added consumer verification for exact release structure, CLI floor, target
  platform, SBOM coverage, offline evidence, workflow/repository/ref/commit,
  monotonic sequence, and exact subject digests.
- Added atomic versioned installation, relative `current` activation,
  install-record/evidence/trust digests, canonical filesystem scope, upgrade,
  exact authorized rollback, and active release removal.
- Added durable `gds release install|upgrade|rollback|remove`
  plan/apply/verify workflows and read-only `release verify`/`release scope`.
- Normalized caller-supplied lifecycle paths to stable absolute inputs before
  they enter a durable plan. Canonical install scope resolves the macOS
  `/tmp` alias to `/private/tmp` before authorization.
- Added `scripts/validate_release.sh`; hosted release is blocked until full Go,
  schema, security, source-freshness, projection, memory, skill, plugin, and
  recorded harness-runtime gates pass.

## Proven locally

- repeated archive and cross-platform Go builds use deterministic inputs and
  reject local paths, VCS metadata, unsupported CPU baselines, or ambient Go
  configuration;
- Go subprocesses and offline `gh` verification do not inherit GitHub tokens or
  the user's Git configuration/HOME;
- the release directory rejects every extra, missing, symlinked, renamed,
  oversized, or digest-mismatched file;
- inner archive traversal, duplicate paths, modes, checksums, aggregate digest,
  executable count, detached manifest, and envelope substitution are detected;
- unpinned trusted root, changed evidence, changed local trust, bad identity,
  insufficient CLI, missing platform, bad SBOM, sequence conflict, and
  unauthorized downgrade are blocked;
- installation rejects parent-path aliasing, record symlinks, undeclared files,
  mode/size/digest drift, and stale active pointers;
- materialize/activate/rollback/remove handlers and the common durable
  plan/apply/verify engine pass lifecycle tests;
- a clean local macOS CLI rehearsal built two independently reproducible
  release units (`0.1.0-canary.2`, sequence 2, and `0.1.0-canary.3`, sequence
  3), verified the independently pinned trusted root, installed sequence 2,
  upgraded to sequence 3, rolled back under one exact expiring scope-bound
  authorization, and removed the active release; every apply and post-verify
  completed with durable operation evidence;
- the rehearsal rejected its first otherwise-valid candidate after detecting
  an ignored `.DS_Store` in a generated Codex plugin package. Packaging now
  excludes deterministic platform/editor/runtime noise and has direct plus
  integration regression coverage; the rebuilt manifests contain none;
- 2000 unique rollout targets remain deterministically allocated once across
  bounded waves, and failing gates cannot advance.

## Estate security finding

The earlier redacted scan of nested legacy metadata repositories reported 10
findings in `nddev-monorepo` and 396 in `forks-monorepo`; 388 of the latter are
from one WebBench CSV dataset. Values were not exposed or copied. These are
outside the portable GDS bundle and remain separate migration evidence until
the underlying repository boundaries are classified or the legacy containers
are retired after parity.

## Not proven

- private-repository attestation entitlement on the active GitHub plan;
- a live run of `.github/workflows/release-bundle.yml`;
- verification of real GitHub-generated provenance and SPDX bundles;
- Linux consumer install/upgrade/rollback/remove execution (Linux binaries are
  reproducibly built, but the lifecycle was exercised on macOS only);
- artifact upload retention/download behavior;
- real canary repository adoption, PR checks, merge, rollback, or wave advance;
- final classification of legacy nested-repository redacted secret findings.

The local CLI rehearsal used `tests/helpers/fake_gh_attestation.py` only to
exercise exact command shape and subject binding against offline files. It is
explicitly test-only and is not evidence of a real GitHub signature.

## External approval required

Running the release workflow, creating or moving a tag, publishing an artifact
or GitHub Release, opening rollout PRs, merging, applying provider settings,
or performing a live rollback requires a separate exact authorization and
current precondition recheck.
