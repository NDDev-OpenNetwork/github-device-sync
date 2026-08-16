# Phase 02 schema and identity evidence

Status: completed locally; no runtime switch and no external mutation.

## Completed

- Added the control-plane repository anchor at `.gds/repository.yaml` with a
  stable typed repository ID and a separate verified GitHub numeric ID and
  owner/name locator.
- Added Draft 2020-12 schemas for repository, estate, policy, harness profile,
  device, mutation plan, operation result, and migration registry contracts.
- Added typed IDs, canonical ULID bounds, exact SHA-1/SHA-256 Git OID shapes,
  closed objects, explicit enums, portable paths, and plan/result invariants.
- Added a migration registry and a reversible legacy-to-v1 mapping contract.
- Added a read-only bootstrap validator with deterministic JSON result
  envelopes and stable exit classes.
- Added valid and expected-invalid fixtures, including repository identity and
  legacy superproject topology round-trip evidence.
- Preserved the legacy Bash runtime unchanged by this phase.

## Evidence

The following commands passed on 2026-07-11:

```text
python3 scripts/validate_gds_schemas.py --json
uv run --no-project \
  --with-requirements requirements/schema-validator.txt \
  python3 scripts/validate_gds_schemas.py --json
python3 -m unittest tests/schema/test_validate_gds_schemas.py
uv run --no-project \
  --with-requirements requirements/schema-validator.txt \
  python3 -m unittest tests/schema/test_validate_gds_schemas.py
ruff format --check scripts/validate_gds_schemas.py tests/schema/test_validate_gds_schemas.py
ruff check scripts/validate_gds_schemas.py tests/schema/test_validate_gds_schemas.py
prettier --check schemas/v1/*.json \
  tests/fixtures/schemas/v1/*.json schemas/v1/README.md
markdownlint-cli2 schemas/v1/README.md \
  schemas/migrations/v0-to-v1/README.md \
  docs/migration/2026-07-gds-migration-plan.md
gitleaks dir --no-banner --redact <each-phase-02-path>
bash -n tools/sync.sh tools/test-sync.sh
shellcheck -x tools/sync.sh tools/test-sync.sh
tools/test-sync.sh
git diff --check
```

Results:

- schema result envelope: `succeeded`, exit code `0`;
- schema/unit tests: `18` passed;
- legacy estate smoke tests: `64` passed, `0` failed;
- secret findings: `0` in Phase 02 paths;
- formatter, linter, shell static checks, and whitespace checks: passed.

## Files added or changed

- `.gds/repository.yaml`;
- `schemas/v1/`;
- `schemas/migrations/`;
- `requirements/schema-validator.txt`;
- `scripts/validate_gds_schemas.py` and `scripts/__init__.py`;
- `tests/fixtures/schemas/v1/`;
- `tests/fixtures/migrations/v0-to-v1/`;
- `tests/schema/` and package markers;
- this evidence record and the active migration plan.

## Not proven

- Stable identities have not been assigned to the other discovered estate
  repositories.
- No migration has been applied to a managed repository other than adding the
  control-plane anchor locally.
- GitHub settings, rulesets, App installations, and permissions remain outside
  this local phase and are not proven by schema validation.
- Public/private generated projection behavior belongs to the compiler phase
  and is not proven here.
- The production CLI implementation stack is not selected by these schemas.

## Risks and containment

- The Python validator is a bootstrap migration gate, not a second permanent
  policy authority. It must become a compatibility wrapper or be retired after
  the production `gds validate schemas` command reaches fixture and envelope
  parity.
- Migration registry entries remain `planned`; no legacy file is deleted or
  rewritten.
- Existing unrelated worktree changes remain present and were not reverted.

## Next dependency

Record the implementation-stack decision from measured local and target
requirements, then implement the Phase 03 read-only CLI: context, status,
discover, inventory, validate, and doctor.

## External approval required

None for local read-only Phase 03 implementation. Explicit approval remains
required before any push, GitHub App installation, provider setting change,
repository rollout, release, or deletion.
