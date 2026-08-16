# Phase 04 policy and projection evidence

Status: completed locally as a non-mutating candidate; release and rollout are
not proven.

Date: 2026-07-11.

Historical scope note: this record describes the Phase 04 candidate before
mutation existed. C1 later added policy exceptions and source maintenance; C2
applied the root projection through journaled plan/apply/verify. Current
evidence is in `docs/migration/c2-controlled-local-cutover-evidence.md`.

## Completed

- Added canonical reusable policies under `policies/` with explicit IDs,
  tiers, priorities, selectors, distribution classes, and monotonic security
  paths.
- Added strict compiled-policy and bundle-lock schemas plus positive,
  schema-negative, and semantic-negative fixtures.
- Added deterministic policy loading and compilation with fixed tier order,
  higher-priority override, equal-priority conflict detection, explicit list
  mutation, selector validation, and per-leaf provenance.
- Added semantic validation for compiled digests, every effective leaf,
  provenance/source consistency, source uniqueness, and source order.
- Added a public/internal/private policy distribution firewall in both the
  compiler and generator.
- Added embedded public-safe templates for concise standalone `AGENTS.md` and
  the Claude `@AGENTS.md` adapter.
- Added deterministic body, full-file, aggregate-output, input, policy, and
  development-bundle digests with ADR 0015.
- Added in-memory generation of four candidate files and read-only drift
  verification that rejects missing, modified, symlinked, non-regular, or
  escaping paths.
- Added golden control-plane projections and byte-for-byte reproducibility
  tests.
- Added `gds compile policy`, `gds generate repository [--check]`, and
  `gds validate projections` without adding a file-write path.

## Runtime evidence

The local development binary returned:

- `gds compile policy`: exit 0, compiled source order
  `repository-default -> control-plane`, with all effective leaves provenanced;
- `gds generate repository`: exit 0, four candidate paths, zero mutation;
- `gds generate repository --check`: exit 2 with expected legacy drift;
- `gds validate projections`: the same deterministic expected drift.

The current check reports two missing generated `.gds` files, the existing
manual `AGENTS.md`, and the existing symlinked `CLAUDE.md`. This is evidence
that drift detection works; Phase 04 does not overwrite or install candidates.
Every command envelope reports `attempted: false` and `completed: false`.

## Verification

The following checks passed on 2026-07-11:

```text
scripts/validate_go_core.sh --quick
go test ./...
go test -race ./...
go vet ./...
go mod tidy -diff
go mod verify
CGO_ENABLED=0 cross-builds: darwin/linux x arm64/amd64
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -show verbose ./...
python3 scripts/validate_gds_schemas.py --json
uv run --with-requirements requirements/schema-validator.txt \
  --with pytest --with pytest-cov python -m pytest tests/schema -q
uv run --with ruff ruff format --check scripts tests/schema
uv run --with ruff ruff check scripts tests/schema
tools/test-sync.sh
npx markdownlint-cli2 Phase-04 documentation
npx prettier --check canonical schema and fixture JSON
bash -n scripts/validate_go_core.sh
shellcheck scripts/validate_go_core.sh
gitleaks dir --no-banner --redact <each Phase-04 path>
git diff --check
```

Results:

- Go unit/integration and race tests passed across all packages;
- Python schema tests: 20 passed;
- legacy estate smoke tests: 64 passed, 0 failed;
- four CGo-free development cross-builds passed;
- golden generation repeated byte-for-byte;
- public marker leakage findings: 0;
- policy visibility bypass attempts: deterministically blocked;
- called-symbol vulnerabilities: 0;
- secret findings across individually scanned Phase 04 paths: 0;
- formatting, vet, module, schema, shell, Markdown, and whitespace gates passed.

The cross-build and race evidence uses local `go1.26.4` and remains
development-only. The full validation command correctly exits 13 before
release evidence because the exact source-registered builder is `go1.26.5`.

## Files added or changed

- `policies/`;
- `templates/`;
- `core/compiler/`;
- `core/projections/`;
- Phase 04 additions to `core/app`, `core/cli`, `core/domain`, and the Git
  adapter;
- `schemas/v1/compiled-policy.schema.json`;
- `schemas/v1/bundle-lock.schema.json`;
- policy/common schema refinements and semantic validators;
- Phase 04 schema fixtures and `tests/golden/projections/`;
- `.gds/repository.yaml` verified command facts;
- ADR 0015 and policy/projection/CLI contracts;
- this evidence record and migration indexes.

## Not proven

- No `.gds/bundle.lock.yaml`, compiled policy, or generated instruction file is
  installed in the active control-plane root.
- Materialization has no plan/apply/verify workflow yet and is intentionally
  unavailable.
- The development bundle is not an immutable released artifact and has no
  SBOM, attestation, release sequence, consumer verification, or anti-rollback
  state.
- The secure exact `go1.26.5` release builder remains unavailable locally.
- Claude import behavior and every other harness projection still require
  versioned runtime contract tests.
- Policy exceptions are not implemented; monotonic weakening therefore has no
  bypass path.
- Only the reusable base and control-plane role policies exist. Other roles,
  owners, portfolios, stacks, and lifecycle policies remain future canonical
  sources.
- No external repository, provider setting, branch, PR, release, or GitHub App
  was changed.

## Risks and containment

- The active root still contains legacy instruction files. Candidate drift is
  reported rather than auto-remediated.
- Development source commit metadata points to the observed baseline HEAD while
  input and source digests capture current uncommitted candidate content. It is
  not represented as an immutable release.
- Claude projection syntax is source-backed design input but remains
  `NOT_PROVEN` at runtime until the harness adapter phase.
- The generator exposes only paths and digests through the CLI; file contents
  remain inside deterministic library tests and golden evidence.

## Next dependency

Implement canonical `gds-*` skills, profile selection, trigger/output/
enforcement eval fixtures, Codex plugin packaging, invocation policy, and
trusted hook contracts without installing them globally or enabling mutation.

## External approval required

None for local Phase 05 implementation. Explicit approval remains required
before installing global plugins or hooks, upgrading the system Go toolchain,
publishing a bundle, pushing, or changing any external repository/provider
state.
