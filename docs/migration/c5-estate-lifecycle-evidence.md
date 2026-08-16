# C5 repository estate lifecycle evidence

Status: accepted local-provider gate

Date: 2026-07-11

Scope: repository, module, fork, device workspace, stable relationship index,
selected consumer, and portfolio planning lifecycles. All mutating fixtures
used temporary Git repositories, local bare remotes, and temporary workspace
roots. No live GitHub, package registry, or public release mutation was enabled
or attempted.

## Completed

- Added atomic repository onboarding from one schema-validated candidate
  anchor. Stable identity, provider origin, clean Git state, policy compilation,
  and the absent target file are rechecked before materialization.
- Added stable identity and relationship indexing with complete-boundary mode.
  Duplicate stable IDs, provider IDs, provider locators, local paths, and
  missing typed targets are hard findings.
- Added provider-first rename, transfer, and archive plans. Stable ID and alias
  history are preserved; local apply is disabled until the C8 provider can
  prove the external transition.
- Added a separately gated deletion plan requiring archived state, exact GDS
  and provider confirmations, complete relationship analysis, zero remaining
  relationships/consumers, and no unanchored boundary.
- Added typed module relationship add/remove transactions, exact gitlink pin
  updates, immutable version-tag publication through the isolated provider,
  and selected-consumer planning. Package and GitHub Release requirements
  remain explicit provider blockers.
- Added deterministic device placement, full versus blob-filtered local clone
  policy, atomic checkout publication, and exact source-anchor verification.
  Checkout removal moves only a proven safe checkout to device quarantine.
- Added fast-forward-only fork synchronization that preserves maintained fork
  commits, exact upstream detachment with history preserved in the anchor, and
  provider-first fork archive planning.
- Added a bounded portfolio aggregate plan with independently digested
  repository subplans. A blocked repository yields `partial` and remains
  visible without erasing ready subplans.
- Added `docs/contracts/lifecycles-v1.md`, aligned CLI/topology documentation,
  refreshed verified Serena provenance, and materialized the control-plane
  projections through the operation engine.

## Force-path proof

The accepted C5 command surface contains no force fork synchronization mode.
`gds fork sync --force` is rejected by Cobra as `GDS_CLI_INPUT_INVALID` before
context resolution or mutation. The only implemented sync handler requires
strict fast-forward ancestry and exact old/new OIDs. It has no reset,
force-push, or fallback branch.

A future force path is therefore a new security-sensitive feature, not an
undocumented option. It must add the design-required exact old OID, recovery
ref, explicit approval, and verification gates before it can enter the command
surface.

## Aggregate isolation proof

The module consumer fixture combines:

```text
one git-submodule consumer -> independent stored update-pin plan
one package consumer       -> explicit provider blocker
```

The result is exit class `partial`, with one planned and one blocked subplan.

The portfolio fixture combines two independent anchored repositories in the
same portfolio:

```text
clean current default repository -> ready subplan
dirty repository                 -> blocked subplan
```

The aggregate retains both subplans, exact stable IDs, target-set digest,
subplan digests, and the aggregate plan digest.

## Control-plane projection operation

```text
state path:
  ~/.local/state/github-device-sync/state.db

plan id:
  plan_01KX8P65RKV6P5VGGJ3JNXTFA9

plan digest:
  sha256:ff660b96c526dba226c7ee14fe43be1495e2f945c3a79b851c68fe75ca7c1634

operation id:
  op_01KX8P65ZYG4BE1H2B76PZZ0R1

input digest:
  sha256:2dddf09111d11731a84ffdf6ba82f929e6790d94eed1c21e77b50db1b8f0de3f
```

Plan, exact approval, apply, and explicit verify succeeded. The operation
journal reports completed mutation, and the subsequent projection check has
zero findings.

## Acceptance gates

| Gate | Evidence | Result |
|---|---|---|
| Rename/transfer preserve identity and relationships | pure transition validator plus provider-first plan fixtures | pass |
| Module publication/final-ref reachability | exact local origin default/tag/gitlink fixtures | pass |
| Shared consumers and mixed consumption isolate failure | selected-consumer aggregate fixture | pass |
| Force behavior cannot bypass preservation | no force command plus explicit negative CLI test | pass |
| Deletion is separately explicit | archived-only complete-analysis delete planner | pass |
| One repository failure remains isolated | portfolio ready/dirty aggregate fixture | pass |
| Workspace placement is deterministic and bounded | device selectors, full/blob-none clone, path and symlink fixtures | pass |
| Checkout removal preserves recovery | deterministic quarantine move and verify fixture | pass |
| Generated projections are reproducible | journaled projection materialization and zero-drift check | pass |
| Memory provenance is current | seven verified memories with matching source digests | pass |

## Verification executed

```text
python3 scripts/validate_gds_schemas.py
scripts/validate_go_core.sh --quick
go test ./... -count=1
go test -race ./core/operations ./core/providers/git ./core/gitops \
  ./core/anchor ./core/repository ./core/workspace ./core/fork \
  ./core/portfolio ./core/app ./core/cli -count=1
go vet ./...
tools/test-sync.sh
gds generate repository --check --json
gds memory validate --json
go test ./core/cli \
  -run 'TestForkSyncRejectsForceModeAtCLIContract|TestForkSyncFastForwardsWithoutForceAndVerifiesExactRefs' \
  -count=1
```

Observed results:

- all Go packages passed twice without cache, including the complete CLI
  integration suite;
- the selected operation, provider, lifecycle, aggregate, app, and CLI packages
  passed under the race detector;
- `go vet` and schema validation passed;
- the legacy estate parity suite passed 64 of 64 checks;
- projection drift is zero after the journaled refresh;
- all seven Serena memories are verified with matching committed provenance;
- the explicit force-mode negative test passed.

## Not proven or intentionally deferred

- Installed Go is `go1.26.4`, below the registered security floor `go1.26.5`.
  Development gates pass, but release evidence remains `NOT_PROVEN` until the
  pinned toolchain is upgraded through its bootstrap transaction.
- Live GitHub repository transitions, deletion, network push/clone, pull
  requests, checks, reviews, merge, and GitHub Release remain disabled until
  C7/C8 acceptance.
- Package registry publication and package-consumer manifest updates remain
  blocked until their providers and supply-chain evidence exist.
- No live GitHub App, repository setting, permission, branch, PR, release,
  package, or deployment was changed.

## Next dependency

Use the accepted onboarding and projection workflows to assign verified anchors
and standalone context to the already correctly placed local source
repositories. Then C6 can implement and behaviorally accept the seventeen canonical
harness adapters without depending on legacy metadata repositories.
