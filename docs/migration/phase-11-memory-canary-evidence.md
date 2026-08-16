# Phase 11 memory and harness-canary evidence

Date: 2026-07-11

Historical scope note: C2 subsequently promoted all seven memories against
committed sources, proved fresh Serena 1.5.3 discovery for Go, Python, and Bash,
materialized the root projections, and ran available harness canaries. See
`docs/migration/c2-controlled-local-cutover-evidence.md` for current evidence.

## Completed

- Changed Serena project languages from Bash-only to Go, Python, and Bash.
- Excluded estate container repositories and migration artifacts from this
  control-plane semantic workspace.
- Replaced four numeric legacy memories with seven semantic memories derived
  from the implemented GDS source.
- Added strict `memory-metadata` schema and deterministic source-digest
  validation.
- Added `gds validate memories` and integrated it into control-plane validation
  and doctor.
- Added the exact seventeen-harness, twelve-case clean runtime contract corpus.
- Added schema and static exact-set validation for that corpus.

## Memory evidence

```text
memory count: 7
status: generated-unverified (7)
source digest matches: 7
invalid numeric names: 0
missing required sections: 0
validation findings: 0
```

The memories intentionally remain `generated-unverified`: their source files
are working-tree changes based on commit
`433c46b6923f7dc1efb96713b9ffc9330ca8ba58`. Promotion to `verified` requires
committing the source, replacing `source_commit`, setting
`source_state: committed`, recomputing each digest, and rerunning validation.

## Harness canary corpus

`tests/harness/runtime-contract.yaml` covers all seventeen canonical harnesses and
requires these twelve evidence lanes:

- clean install;
- bounded binary/version detection;
- root and nested instruction discovery;
- exact skill discovery;
- explicit read-only invocation;
- destructive implicit negative;
- hook lifecycle;
- public/private context firewall;
- generated projection drift;
- update and rollback;
- removal.

The corpus is a test contract, not a runtime result.

## Verification

```text
gds validate memories --json: pass, 7 memories, 0 findings
python schema/fixture validation: pass
go test ./core/memory ./core/harness ./core/validation ./core/cli: pass
scripts/validate_go_core.sh --quick: pass, development-only
race suite for memory/harness/skills/projections/app/CLI/validation: pass
legacy estate smoke suite: 64/64 pass
Gitleaks 8.30.1 on affected canonical roots: no findings
```

The full release gate remains unavailable because local `go1.26.4` is below
the registered secure release-builder floor `go1.26.5`.

## Not proven

- The active Serena 1.5.3 MCP process still reports the cached Bash-only
  language configuration. It must be restarted before Go symbol discovery can
  be accepted.
- No interactive harness was installed or reconfigured for canary execution.
- The twelve runtime cases have not passed for any exact harness/model pair.
- The current root legacy instruction projections have not been replaced; GDS
  generation still reports their drift and will not overwrite them silently.

## Next dependency

Restart Serena/Codex, prove Go symbol discovery, then execute isolated canaries
per available harness. Only after representative discovery parity may GDS
materialize the generated root projections and retire legacy bridges through an
explicit local plan.
