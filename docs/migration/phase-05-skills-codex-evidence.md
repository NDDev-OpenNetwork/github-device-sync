# Phase 05 skills and Codex evidence

Status: static implementation complete; Codex runtime behavior is
`NOT_PROVEN`.

Date: 2026-07-11.

Follow-up: C2 proved Codex root/nested instruction discovery but did not
install GDS packages. Skill, hook, explicit-only, visibility, and model eval
acceptance remains C6 after C3-C5 command parity.

## Completed

- Added one canonical registry for 23 `gds-*` skills, five scope profiles, three
  Codex packages, invocation policy, mutation classification, interface
  metadata, and budgets.
- Added all 23 portable `SKILL.md` procedures and matching Codex sidecars.
- Marked every externally mutating workflow explicit-only; no implicit routing
  control is treated as authorization.
- Added strict skill-registry schema and fixture coverage.
- Added a deterministic Go validator for registry, paths, frontmatter,
  descriptions, sections, sidecars, profiles, budgets, and core eval inputs.
- Added standalone in-memory plugin packaging with ordered file digests and a
  generated package manifest. Plugin source contains no copied canonical
  skills.
- Added `gds-core`, `gds-estate-admin`, and `gds-module` manifests plus a
  repository marketplace.
- Added one lifecycle hook owner in `gds-core`, with bounded SessionStart,
  PreToolUse, and Stop handlers.
- Added a provisional Codex capability profile sourced from current official
  documentation.
- Added 80 core trigger queries: 40 positive and 40 near-miss negative, each
  configured for three runs, plus core output assertions.
- Added `gds validate skills`, `gds validate plugins`, `gds skill package
  <plugin>`, and `gds validate harnesses --harness codex`.

## Static evidence

The following local commands passed:

```text
go test ./core/skills ./core/harness ./core/app ./core/cli ./core/validation
python3 -m unittest tests.harness.test_codex_hook -v
python3 scripts/validate_gds_schemas.py --root . \
  --fixtures tests/fixtures/schemas/v1/cases.json --json
python3 <skill-creator>/quick_validate.py <each canonical skill>
python3 <plugin-creator>/validate_plugin.py <each source plugin>
gds validate skills --json
gds validate plugins --json
```

Observed deterministic package candidates:

```text
gds-core          8 skills
gds-estate-admin 12 skills
gds-module        3 skills
```

The duplicated `gds-triage-estate-drift` profile membership is deduplicated in
the estate-admin package. Repeated package generation produced identical
candidate JSON and file digests. Temporary standalone materialization tests
used new destinations and did not install a plugin.

## Runtime evidence

`gds validate harnesses --harness codex --json` intentionally returns:

```text
exit 3
GDS_HARNESS_RUNTIME_NOT_PROVEN
```

This is correct. No fresh isolated Codex session has yet proven:

- exact instruction-chain discovery;
- exact installed skill set from root and nested directories;
- explicit invocation of every packaged profile;
- non-invocation of destructive skills from near-miss prompts;
- hook discovery and trust behavior;
- standalone and embedded public/private context fixtures.

The old active Codex installation is not accepted as evidence for the new GDS
profile.

## Security review

- Plugin packaging rejects symlinked source roots, copied/generated source
  skills, escaping paths, oversized files, and unknown profile references.
- Materialization writes through a confined `os.Root` into a new destination.
- The hook reads at most 1 MiB, emits bounded context, uses timeouts, requires an
  absolute non-symlink `GDS_BIN`, and passes an environment allowlist.
- The hook blocks only explicit high-risk command shapes and is documented as
  incomplete defense in depth.
- No secret, credential, transcript, token, remote content, or estate inventory
  is packaged.

## Files added or changed

- `skills/registry.yaml`, `skills/canonical/`, and `skills/evals/`;
- `schemas/v1/skill-registry.schema.json` and schema fixtures;
- `core/skills/` and `core/harness/`;
- `plugins/gds-core`, `plugins/gds-estate-admin`, and `plugins/gds-module`;
- `.agents/plugins/marketplace.json`;
- `harnesses/capability-registry.yaml` and `harnesses/codex/profile.yaml`;
- Phase 05 additions to app, CLI, validation, tests, contracts, source register,
  and migration evidence.

## Not proven

- Codex runtime discovery and model-dependent eval results.
- Admin, module, device, and portfolio trigger/output corpora beyond static
  routing boundaries embedded in their skills.
- Plugin installation, hook trust, update, rollback, and uninstall.
- Any other harness adapter.
- A released, attested bundle or plugin artifact.
- The exact secure Go 1.26.5 release builder.

## Next dependency

Implement the local state store, append-only journal, locks/leases, plan digest,
stale-state rejection, and the first side-effect-free plan/apply/verify engine.
No external mutation is enabled by Phase 05.

## External approval required

None for the local Phase 06 implementation. Explicit approval is required
before installing or trusting the new plugin/hooks, updating the system Go
toolchain, publishing an artifact, pushing, or changing external state.
