# GDS public engine instructions

This repository is the open-source GDS engine and a reusable module. It owns
generic schemas, compilers, transactions, adapters, templates and synthetic
fixtures. It is not the NDDev estate and must never acquire real organization,
tenant, person, host, network, credential-reference or production evidence.

A separately managed private estate pins this repository at
`modules/github-device-sync` and owns its private policy and topology.
External-estate behavior must bind the estate commit, module lock and exact GDS
gitlink while verifying that embedded templates match the pinned engine
checkout.

## Source map

- CLI and command wiring — `core/cli`
- Use-case orchestration — `core/app`
- Transactions and recovery — `core/operations`
- Policy compilation — `core/compiler`
- Projection generation — `core/projections`
- Provider adapters — `core/providers`
- Public schemas — `schemas/v1`
- Generic templates — `templates`
- Synthetic example estate and policies — `estate`, `policies`
- Task procedures — `skills/canonical`

## Working contract

- Treat each Git repository as an independent mutation boundary and preserve
  unrelated branches, worktrees, submodules and dirty changes.
- Prefer direct technical progress and executable invariants over ceremonial
  gates. Research uncertain implementation details and fix root causes.
- Provider changes remain journaled and exactly verified because that protects
  correctness and recovery, not because an agent needs procedural supervision.
- Public workflows use GitHub-hosted runners only. Never expose estate runtime,
  self-hosted labels or private evidence through public refs.
- Keep secrets, runtime state, caches, logs and generated evidence untracked.
- Generated fixtures under `tests/golden` change only through their generator.

## Verification

- Lint: `scripts/validate_shell.sh`.
- Lint: `scripts/validate_go_core.sh --quick`.
- Test: `go test ./...`.
- Test: `python3 -m pytest`.
- Build: `go build -trimpath ./core/cmd/gds`.
- Fast: `scripts/validate_go_core.sh --quick`.
- PR required: `go test ./...`.
- PR required: `python3 -m pip install --quiet --require-hashes -r requirements/test.txt`.
- PR required: `python3 -m pytest`.
