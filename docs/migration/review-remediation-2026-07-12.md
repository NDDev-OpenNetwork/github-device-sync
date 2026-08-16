# Review remediation record — 2026-07-12

## Scope

- Reviewed hypothesis baseline: `main@e082685c47d4c1c547e6c9bba477af1049eca3d2`.
- Implementation branch baseline for this pass: `c19a128736cc91d8c62139b76448acdb94c5a05f`.
- External writes, release publication, deployment, canary, and estate rollout remained disabled.

## Finding disposition

| ID | Disposition | Evidence and resolution |
|---|---|---|
| `RVR-P1-001` | confirmed | Production `--gh-binary` and PATH-only trust were removed. Consumer trust now pins verifier name, version, platform, and executable digest; acceptance evidence records absolute path and identity. |
| `RVR-P2-001` | confirmed | Replay now returns success only for `succeeded`; incomplete terminal and active states return explicit nonzero recovery/conflict classes. |
| `RVR-P2-002` | confirmed | The scheduler rechecks installation blocking after semaphore acquisition; a deterministic interleaving test proves the learned block is observed. |
| `RVR-P2-003` | confirmed | Installation inventories are staged and the aggregate estate bound is validated before any sink persistence. |
| `RVR-P2-004` | confirmed; provider apply blocked | Read-only GitHub evidence differs from the managed squash-only policy. Exact proposed changes are listed below; no provider mutation was performed. |
| `RVR-P2-005` | confirmed | Verification is now one canonical four-tier contract. One generated workflow owns hosted `fast` and `pr-required` checks at one reusable-workflow SHA; duplicate manual callers were removed. |
| `RVR-P2-006` | confirmed | Queue errors are handled before `Processed`, counted and logged. Readiness depends on state-store access and worker/reconciler/backup health; health remains process liveness. |
| `RVR-P3-001` | confirmed | Targeted reconciliation obtains a fresh injected-clock value at each terminal write; a deterministic duration assertion covers success. |
| `RVR-P3-002` | confirmed | Direct inputs and complete transitive locks are separated; every locked package has SHA-256 hashes and CI installs with `--require-hashes`. |

## Canonical verification tiers

- `fast`: quick Go/core/schema/projection validation plus legacy sync parity.
- `pr-required`: exact Go release toolchain, full Go/race/cross-build validation, Python tests, integrated assurance, and legacy parity.
- `full`: `pr-required` scope with the full assurance race lane.
- `release`: `full` plus release-specific source, visibility, artifact, and harness-runtime gates.

The structured owner is `.gds/repository.yaml`; executable tier behavior is
`scripts/validate_ci_tier.sh`; hosted projection source is
`templates/github-actions/go.yml.tmpl`.

## Read-only GitHub governance comparison

Observed on 2026-07-12 through `GET /repos/example-user/github-device-sync`:

| Setting | Desired managed value | Observed value | Proposed external step |
|---|---:|---:|---|
| merge commits | `false` | `true` | disable |
| rebase merge | `false` | `true` | disable |
| squash merge | `true` | `true` | none |
| auto-merge | `false` | `false` | none |
| update branch | `true` | `false` | enable |

The proposed provider plan is blocked until the owner separately authorizes an
exact GitHub settings mutation. Re-observation and stale-state checks are
required immediately before any future apply.

## Remaining external evidence

- Immutable release and hosted attestations are not published.
- GitHub App runtime, deployed controller, live webhook delivery, canary, and
  estate rollout remain `NOT_PROVEN`.
- Harness support remains static unless a current native runtime evidence file
  passes the applicable profile gates.
