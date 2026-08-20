<!--
GENERATED FILE - DO NOT EDIT DIRECTLY
generator: gds
bundle: 0.4.0-dev
source-tree-digest: sha256:0000000000000000000000000000000000000000000000000000000000000001
input-digest: sha256:3e9e73dccf4e3f5cf078d310b59a5c9157707afe33430290a0fc41b38df28781
output-digest: sha256:c034627a923a5b8b93034977cfefc2a8cd1480652933c7ac901c165b32f3dfbe
edit-source:
  - .gds/repository.yaml
  - policies/base/repository-default.yaml
  - policies/repositories/github-device-sync.yaml
  - policies/roles/control-plane.yaml
  - templates/agents/repository.md.tmpl
  - templates/github-actions/go.yml.tmpl
  - templates/harnesses/claude.md.tmpl
-->
# Repository brief

GDS is the public engine for a multi-owner GitHub estate. It loads an external estate root, compiles deterministic projections and executes journaled, content-addressed provider transactions.

## What it does

- Compile canonical estate intent into deterministic per-repository projections
- Resolve where a checkout sits in the estate and which policy governs it
- Plan, approve, apply and verify GitHub changes as recoverable transactions
- Build, attest and install immutable releases with offline verification
- Render harness adapters for agent tooling

## Where to change what

- Generic public policy defaults and synthetic examples — `policies`
- Complete synthetic example estate — `estate`
- What a generated projection says — `templates`
- A typed contract or its validation — `schemas/v1`
- Command surface and flags — `core/cli`
- Use-case orchestration behind a command — `core/app`
- GitHub reads, writes and their failure staging — `core/providers/github`
- Plan, approval, lock and journal semantics — `core/operations`
- Projection identity and rendering — `core/projections`

## How to verify

- Lint: `scripts/validate_shell.sh`
- Lint: `scripts/validate_go_core.sh --quick`
- Test: `go test ./...`
- Test: `python3 -m pytest`
- Build: `go build -trimpath ./core/cmd/gds`
- Fast: `scripts/validate_go_core.sh --quick`
- PR required: `go test ./...`
- PR required: `python3 -m pip install --quiet --require-hashes -r requirements/test.txt`
- PR required: `python3 -m pytest`

## Working here

- Generated files carry a `GENERATED FILE` header. Change the canonical input
  named in `edit-source` and regenerate; editing the output detaches it from
  `.gds/bundle.lock.yaml`.
- One Git repository is one mutation boundary. Work that crosses repositories
  starts with `gds context --json`; work inside this one does not need it.
- Provider writes go through plan → approve → apply and are journaled.
- Task-specific procedures live in `skills/canonical/<name>/SKILL.md`; the
  profiles active here are `core, estate-admin`. Load one when the task
  matches it.

## Facts

- Repository `repo_01M0EZ7TB3KNXNSP78Z8M64WXG`, roles `control-plane`, bundle `0.4.0-dev`.
- Canonical inputs: `.gds/repository.yaml`; compiled result: `.gds/compiled-policy.json`.
- Visibility `public`, data `public`.
