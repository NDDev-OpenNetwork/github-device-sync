# GDS architecture

This is the current architecture overview of the `github-device-sync` (gds)
control plane. It carries no release version in its title deliberately: it
describes the system as it is now, and a version stamped here goes stale
silently every time a release ships without an architecture change. Release
boundaries live in `CHANGELOG.md` and `docs/version-ledger.md`. For the
migration design baseline that preceded the Go implementation, see
the ADRs in `docs/adr/`.

## What gds is

GDS is the agent-first control plane for a multi-owner GitHub repository
estate. It separates repository **identity**, **classification**, **Git
topology**, **device placement**, **observed state**, and **agent context** so
none of those concerns is inferred from a directory tree. Every mutation is a
stored, content-addressed plan that is approved, lock-guarded, rechecked, and
journaled.

## Canonical pipeline

```text
canonical source (YAML)
  -> deterministic policy and context compiler
  -> immutable bundle and repository-local projections
  -> profiled agent harness adapters
  -> plan / approval / apply / verify / journal
```

Source files are the authority; generated files (projections) carry
`GENERATED FILE` headers and are content-addressed by digest. The
deterministic compiler merges policy sources in a fixed precedence
(`base, owner, portfolio, role, stack, lifecycle, repository`) with per-leaf
provenance, and rejects equal-priority selector ambiguity.

## Estate (current)

- **Five GitHub installations**: `example-user` (user), `example-org`,
  `example-media`, `NDDev-OpenNetwork`, and `example-guild`
  (read-only member, no mutation capability). Four carry separate Mutation Apps
  with distinct identity and secret locators from their read Installation Apps.
- **Three devices**: `example-workstation` (linux/x86_64, desktop),
  `example-user-mac2` (macos/arm64, desktop), and `example-user-ubuntu-1`
  (linux/x86_64, desktop-builds, rootful Docker).
- **Repository visibility**: public. The control-plane repository carries
  `visibility_contract: public` and declares no submodules, so no module
  visibility has to be reconciled against it.
- **Posture**: `mutation_mode: "pull-request"` for managed NDDev sources;
  every other selector remains `observe-only`, and every write remains gated by
  exact signed approval plus one-shot enablement.
- **Selectors** classify observed repositories by owner, fork flag, name
  prefix, and archived state into portfolios. Priority bands: `100` generic,
  `200` specialized non-fork, `300` state override (archived precedence).

## Package layout (Go core)

| Layer | Packages | Role |
|---|---|---|
| Primitives | `canonicaljson`, `gitauthority`, `semver`, `serialization`, `validation`, `identity`, `capabilities` | Bounded vocabulary, content-addressing, schema gate. |
| Domain | `domain`, `manifest`, `bundle` | Result envelope, anchor loader, release-unit contracts. |
| Local facts | `context`, `estateregistry`, `discovery`, `inventory`, `security` | Scope resolver, Git-boundary discovery, observed inventory. |
| Policy + compilation | `estate`, `compiler`, `projections`, `materialize` | Desired-state compiler, policy merge, deterministic projection render. |
| Operations | `operations`, `gitops`, `anchor`, `workspace`, `module`, `fork`, `portfolio` | The plan/apply/verify saga engine and typed handlers. |
| Providers | `providers/git`, `providers/github`, `githubruntime`, `githubmutationruntime` | Read-only-by-default git; GitHub App token + pagination; separate write binding. |
| Agent contracts | `skills`, `harness`, `memory` | Canonical skills, 17 harness adapters, provenance-bearing Serena memories. |
| Release + rollout | `releasebuilder`, `releaseconsumer`, `rollout`, `assurance` | Immutable bundle assembly, offline attestation verification, canary/wave rollout, release gate. |
| Control-plane service | `state`, `controller`, `reconciler`, `webhooks`, `audit` | SQLite journal, webhook worker, drift reconciler, signed audit snapshots. |
| Orchestration | `app`, `cli`, `cmd/*` | Use-case wiring, Cobra adapter, seven binaries. |

Six binaries: `gds`, `gds-controller`, `gds-assurance`,
`gds-performance-evidence`, `gds-release-builder`, and
`gds-{claude,codex}-runtime-driver`.

## Verification

- `scripts/validate_go_core.sh` — Go test, schema validation, projection check.
- `scripts/validate_ci_tier.sh {fast,pr-required,full,release}` — tiered CI gate.
- `scripts/validate_assurance.sh` — the 2000-repository offline release scenario.
- `core/assurance` — 16 checks, 13 budgets; the release gate is intentionally
  stricter than the development gate.

## Reading trail

For a fresh agent or contributor, read in this order:

1. This document — what the system is and how its pipeline fits together.
2. `estate/estate.yaml`, `estate/installations/`, `estate/devices/` — ground
   truth, not docs.
3. `docs/contracts/estate-v1.md` — the estate contract (selectors, priority
   bands, visibility).
4. `docs/runbooks/bootstrap-device.md`, `self-hosted-ci-runners.md`,
   `github-ruleset-reconcile.md` — operational seams.
5. `docs/adr/README.md` — why the current shape was chosen, and what was
   rejected.

`CHANGELOG.md` is deliberately not on that trail. It is release archaeology:
accurate, but it narrates two weeks of incidents and their repairs, and reading
it first spends a fresh agent's attention on history it does not need to make
the next change. Consult it when you need to know when something changed or
why a specific fix exists.

The `gds-orient` skill is the runtime entry point: run it inside any anchored
repository to resolve the full estate scope, boundaries, and effective policy.
