# Phase 03 read-only CLI evidence

Status: completed locally for development; release evidence remains blocked.

Date: 2026-07-11.

## Completed

- Selected Go as the additive production core in ADR 0014 without removing or
  switching the legacy Bash/Python runtime.
- Added a typed result envelope, stable exit classes, repository values, and
  process-level exit ownership.
- Added strict JSON/YAML decoding and offline embedded Draft 2020-12 schema
  validation with Python fixture-oracle parity.
- Added a cancellable read-only Git adapter with an explicit subcommand
  allowlist and bounded output.
- Added deterministic local context resolution, stable repository identity,
  standalone/embedded mode, estate registration, bundle-lock status, and skill
  profile routing.
- Added bounded local discovery and ephemeral inventory compilation.
- Added `gds context`, `status`, `discover`, `inventory`, `validate`, and
  `doctor` with machine-readable output.
- Added real Git fixtures for dirty, staged, untracked, conflicted, detached,
  unborn, ahead, behind, diverged, submodule, and linked-worktree states.
- Added a black-box read-only matrix covering every current command and the
  complete isolated repository tree, including `.git` state.
- Added a source-backed security floor and exact release-builder gate.

## Runtime evidence

The local development binary produced these results in the control-plane
repository:

<!-- markdownlint-disable MD013 -->

| Command | Exit | Result | Confirmed fact |
|---|---:|---|---|
| `gds context` | 3 | `not-proven` | Local scope resolved; bundle lock absent |
| `gds status` | 3 | `not-proven` | Git state resolved; bundle lock absent |
| `gds discover --max-depth 1` | 0 | `succeeded` | Four local boundaries: L1 plus three L2 containers |
| `gds inventory --max-depth 1` | 0 | `succeeded` | Four observed entries compiled in memory |
| `gds validate schemas` | 0 | `succeeded` | Embedded schemas and fixture corpus valid |
| `gds validate repository` | 0 | `succeeded` | Control-plane anchor valid |
| `gds doctor` | 3 | `not-proven` | Verified checks returned; bundle lock remains explicit |

<!-- markdownlint-enable MD013 -->

Every envelope reported:

```json
{
  "mutation": {
    "attempted": false,
    "completed": false
  }
}
```

The discovery count matches the legacy root topology: the root repository and
the three configured container boundaries. It does not claim provider or full
estate parity; those require later provider discovery.

## Verification

The following checks passed on 2026-07-11:

```text
scripts/validate_go_core.sh --quick
go test ./...
go vet ./...
go test -race ./...                         development builder only
go mod tidy -diff
go mod verify
CGO_ENABLED=0 darwin/linux cross-builds      development builder only
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -show verbose ./...
python3 scripts/validate_gds_schemas.py --json
uv run --with-requirements requirements/schema-validator.txt \
  --with pytest --with pytest-cov python -m pytest tests/schema -q
tools/test-sync.sh
bash -n scripts/validate_go_core.sh
shellcheck scripts/validate_go_core.sh
markdownlint-cli2 Phase-03 documents
git diff --check
gitleaks dir --no-banner --redact <each Phase-03 path>
```

Results:

- Go tests, vet, race detector, module verification, and four target
  cross-builds passed;
- Python schema tests: 20 passed;
- legacy estate tests: 64 passed, 0 failed;
- black-box command mutation attempts: 0;
- called-symbol vulnerabilities: 0;
- secret findings in individually scanned Phase 03 paths: 0;
- formatting, shell static checks, Markdown lint, and whitespace checks passed.

The race and cross-build results were produced by `go1.26.4` and therefore
prove development portability only. `scripts/validate_go_core.sh` correctly
returns exit 13 before full release validation because `go1.26.5` is required.

## Files added or changed

- `go.mod` and `go.sum`;
- `core/`;
- `schemas/embed.go`;
- `scripts/validate_go_core.sh`;
- `docs/adr/0014-go-production-core.md`;
- `docs/contracts/cli-v1.md`;
- `docs/source-register/`;
- `.serena/research/2026-07-gds-production-stack.md`;
- this evidence record and the Phase 03 security review;
- the active migration plan and architecture index.

## Not proven

- No `.gds/bundle.lock.yaml` exists yet; that is a Phase 04 output.
- The secure exact `go1.26.5` release builder is not installed or verified
  locally, so release artifacts, SBOM, provenance, and attestations are not
  proven.
- GitHub App authentication, provider inventory, remote freshness, PR/check
  state, settings, rulesets, and mutation permissions are not implemented.
- Full estate parity beyond the root plus three local container boundaries is
  not claimed.
- State storage, locks, operation journals, plan/apply/verify, compiler,
  projections, skills, plugins, hooks, and rollout remain later phases.
- No other repository has been onboarded to the v1 anchor or changed.

## Risks and containment

- The Python validator temporarily duplicates execution, not policy; schemas
  and fixtures remain the data-contract authority.
- Remote freshness is always `unknown` because this phase performs no fetch or
  provider request.
- The release gate fails closed on the vulnerable local toolchain; quick mode
  emits a visible warning rather than presenting development evidence as a
  release result.
- All implementation is additive and the legacy commands remain available.

## Next dependency

Implement Phase 04 policy precedence and provenance, deterministic standalone
projections, the immutable bundle lock, golden fixtures, reproducibility, and
manual-drift detection.

## External approval required

None for local Phase 04 implementation. Explicit approval remains required
before installing or upgrading a system toolchain, publishing a bundle,
pushing changes, changing GitHub configuration, or rolling out to another
repository.
