# C2 controlled local projection cutover evidence

Status: accepted locally; full harness adapter acceptance remains C6

Date: 2026-07-11

## Outcome

The control-plane repository is now a valid managed GDS repository. Its
repository anchor, compiled policy, bundle lock, `AGENTS.md`, and first-class
`.claude/CLAUDE.md` agree cryptographically and are materialized only through
the journaled local operation engine.

No GDS plugin, hook, or incomplete skill package was installed or trusted
globally. Runtime activation remains intentionally gated until the commands
referenced by the packages exist in C3-C5.

## Applied projection

The final cutover used:

```text
plan:              plan_01KX87Q99DAA4KY2JNN9CVPJVX
operation:         op_01KX87Q9K85FGZWADF5KA5GS90
approval class:    local-projection-write
approval ref:      owner-request:gds-full-migration
source commit:     4a867c2da59febf59a6d1e30979d205b6e76d7cc
bundle digest:     sha256:14bf4ba32947ff54f52145d80fc0886007d32f9f5a7f69a4c579827348ad7157
input digest:      sha256:aaf4db4c135ce1448122ee0debae0abb6fba8259994d2704453931f01dc030c5
output digest:     sha256:f8437c1c4169026a6b92cf9f67e5d23abbaf595c1c6187cb70b311c9e5e10d81
projection commit: e0b570cb4b249d84f79a23180365eb863006ad4d
```

Plan, apply, immediate verification, and explicit verification all succeeded.
Two consecutive in-memory generations were byte-identical. A subsequent
`gds generate repository --check` returned success with zero findings.

`gds context` now validates the bundle-lock schema, every locked file digest,
compiled-policy identity, bundle version, and policy digest. The tamper fixture
fails closed. `TestMaterializerRollsBackEarlierFileWhenLaterFileIsUnsafe`
proves that a mid-set failure restores the exact earlier file and leaves the
unsafe symlink target unchanged.

## Local state

The first explicit repository-projection plan initialized the private state
store needed to journal itself. The final inspected state was:

```text
path:           ~/.local/state/github-device-sync/state.db
directory mode: 0700
database mode:  0600
schema:         3
journal:        WAL
foreign keys:   enabled
inspection:     query-only
plans:          9
operations:     8
steps:          8
events:         52
locks:          0
```

This snapshot is observed evidence, not desired configuration.

## Instruction discovery

Static Codex inspection recorded:

- `~/.codex/AGENTS.md`: 9125 bytes,
  `03acafa6e5549df0e1f80478e7e6334e58bc6510fda1c970a3be18ffbad527f3`;
- repository `AGENTS.md`: 1879 bytes,
  `5a076d37ee798169b114667e514d35a5212aaf7c3e95a5fa441b37670dc9fa9d`;
- `.claude/CLAUDE.md`: 2219 bytes,
  `a64d71b88271ef77eed9c309efc62bfd1b43a38bef22580f684e10dff819a9fe`.

The normal Codex global-plus-root chain is 11004 bytes. The longest possible
tracked root-to-CWD chain is the repository root followed by the projection
golden fixture: 3758 bytes, below the 24 KiB GDS alert and 32 KiB Codex limit.
No active override or byte-identical duplicate was found.

An ephemeral Git repository then proved discovery without tools:

- Codex `0.144.1` returned the exact root marker and, from a nested CWD, the
  exact nested marker. Its JSONL contained no tool-call event.
- Claude Code `2.1.206` returned the exact marker from first-class
  `.claude/CLAUDE.md` with built-in tools disabled and session persistence off.

The canary directory and its temporary Serena registration were removed after
the run.

## Harness canary classification

Every locally observed harness was invoked in a separate read-only canary
workspace. A binary version is not counted as instruction discovery.

- Codex `0.144.1`: pass; exact root and nested markers, no tool calls.
- Claude Code `2.1.206`: pass; exact first-class Claude marker, tools disabled.
- Antigravity CLI `1.1.1`: `NOT_PROVEN`; bounded print mode timed out.
- MiMo Code `0.1.5`: `NOT_PROVEN`; provider returned HTTP 401 invalid key.
- OpenCode `1.17.18`: `NOT_PROVEN`; provider returned a server error.
- ZCode `0.15.2`: `NOT_PROVEN`; headless turn failed with a trace ID.

Cursor CLI, Kimi Code, and Pi were absent from `PATH`. The observed Grok
wrapper could not resolve its runtime. They were classified `NOT_PROVEN` and
were not presented as locally available canary targets.

Full discovery, skill, explicit-only, hook, visibility, lifecycle, and model
eval suites remain C6 work. The current result does not promote any provisional
harness profile to supported.

## Serena and language discovery

A fresh ephemeral Codex process used only Serena MCP tools and proved:

```text
Serena version: 1.5.3
project: github-device-sync
backend: LSP
languages: go, python, bash
memories: 7 verified semantic memories
```

Local language-server evidence was also present for `gopls v0.22.0`,
BasedPyright `1.39.9`, and Bash Language Server `5.6.0`.

## Verification

The following gates passed:

```text
scripts/validate_go_core.sh --quick
uv run --with-requirements requirements/test.txt --with pytest-cov \
  python -m pytest tests/schema -q
tools/test-sync.sh
go test -race ./core/context ./core/harness ./core/projections ./core/materialize
gds validate skills
gds validate plugins
gds validate harnesses --harness all
gds memory validate
gds generate repository --check
git diff --check
repository-wide retired-name scan
```

Results:

- Go quick validation passed across the complete core;
- Python schema tests: 23 passed;
- legacy parity tests: 64 passed, 0 failed;
- targeted race tests passed;
- 23 skills, five profiles, and three deterministic Codex packages validated;
- seven Serena memories are verified against committed sources;
- no forbidden retired harness identity or bridge filename remains in the
  repository.

Go `1.26.4` remains below the registered `1.26.5` release floor. Development
evidence is valid; release evidence remains `NOT_PROVEN`.

## Remaining boundary

- C3-C5 must implement the transaction, Git workflow, and lifecycle commands
  referenced by canonical skills before any GDS package is activated.
- C6 owns adapter install/update/remove/doctor, clean runtime suites, and model
  evals for all seventeen harnesses.
- The control-plane checkout remains at its current path until final clean
  synchronization can update every device pointer atomically.
- No GitHub App, provider setting, PR, merge, release, or estate rollout was
  changed by C2.
