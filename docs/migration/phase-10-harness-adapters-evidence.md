# Phase 10 harness adapter evidence

Date: 2026-07-11

## Completed

- Replaced the incomplete eight-entry registry with the exact ten
  owner-selected canonical harness identities.
- Added strict registry and expanded capability-profile schemas.
- Added a versioned profile for every target with official sources, instruction
  behavior, skill paths, explicit-only mechanism, projection strategy, hooks,
  and runtime gate.
- Generalized `gds validate harnesses` from Codex-only to one or all profiles.
- Added bounded read-only `gds harness detect`.
- Replaced the mechanical Claude import with a standalone generated
  `.claude/CLAUDE.md` compiled from the same typed inputs as `AGENTS.md`.
- Added portable `disable-model-invocation: true` to every canonical
  explicit-only skill while preserving Codex sidecars.
- Completed one transactional adapter lifecycle for all seventeen targets: render,
  inspect, plan-install, apply, verify, update, exact rollback, remove, and
  doctor, with unmanaged-state preservation and drift rejection.
- Added a fail-closed native runtime driver protocol and strict evidence
  ingestion. Evidence is bound to the exact executable/version, model,
  execution profile, tool set, platform, capability profile, runtime contract,
  case set, metric set, and confined transcript files.
- Added schema-validated trigger and output corpora for all five canonical
  skill profiles plus one common critical-enforcement corpus. Coverage is
  recomputed from the exact sample/run identities; aggregate claims cannot
  replace missing transcripts.
- Updated source register, architecture, ADRs, CLI, skill, and projection
  contracts.

## Local runtime evidence

The bounded version detector observed:

| Harness | Observation |
|---|---|
| Antigravity CLI | `1.1.1` |
| Claude Code | `2.1.206 (Claude Code)` |
| Codex | `codex-cli 0.144.1` |
| MiMo Code | `0.1.5` |
| OpenCode | `1.17.18` |
| ZCode | `0.15.2` |
| Cursor CLI | binary not proven |
| Kimi Code | binary not proven |
| Pi | binary not proven |
| Grok CLI | wrapper present; version command failed |

These observations are local runtime facts, not support claims. No global
configuration, authentication, plugin, hook trust, or home-directory state was
changed.

Migration executor evidence for this phase:

```yaml
harness: codex
harness_version: "codex-cli 0.144.1"
model_label: "NOT_PROVEN"
execution_profile:
  approval_policy: "never"
  sandbox_mode: "danger-full-access"
tools_observed:
  - filesystem-and-shell
  - web
  - serena-mcp
```

The model label is intentionally not inferred from repository configuration or
owner preference because the active runtime did not expose authoritative model
identity evidence.

## Static verification

```text
python3 scripts/validate_gds_schemas.py --root . --fixtures tests/fixtures/schemas/v1/cases.json --json
go test ./core/harness ./core/validation
go test ./core/harness ./core/app ./core/cli ./core/validation
go test ./core/projections ./core/skills
go test -race ./core/harness ./core/skills ./core/projections ./core/app ./core/cli ./core/validation
scripts/validate_go_core.sh --quick
tools/test-sync.sh
gds validate harnesses --harness all --json
gds harness detect --harness all --json
gitleaks detect --no-git --redact --source <canonical-phase-root>
```

Results at this checkpoint:

- schema/fixture validation: pass;
- Python and embedded schema suites: pass;
- Go core quick gate (`gofmt`, tidy diff, module verify, vet, all packages,
  schemas): pass, development-only;
- race suite for harness, skills, projections, app, CLI, and validation: pass;
- legacy estate smoke suite: 64/64 pass;
- Go package tests listed above: pass;
- exact registry: seventeen profiles and two non-predecessor migration aliases;
- static profile validation: only seventeen expected
  `GDS_HARNESS_RUNTIME_NOT_PROVEN` findings;
- runtime detection: six versions observed and four explicit `NOT_PROVEN`
  outcomes;
- deterministic lifecycle fixtures: install/update/rollback/remove pass for all
  ten adapters and preserve unrelated files;
- native driver execution was not attempted; external model/tool mutation is
  therefore false/false.
- Gitleaks 8.30.1 across `core`, `schemas`, `harnesses`, `templates`, `skills`,
  and the affected ADR/contract/migration/source-register roots: no findings.

The first quick-gate attempt reported one unformatted touched Go file. The file
was formatted with `gofmt`, then the complete quick gate passed. No logic or
test failure remained.

## Not proven

- Clean runtime instruction and skill discovery for every exact version.
- Native execution of the complete trigger/output/enforcement corpora.
- Harness-specific hook execution and trust.
- Exact Cursor and MiMo CLI skill discovery transcripts; the MiMo projection
  remains based on official AGENTS behavior plus bounded local discovery.
- Installed Kimi Code, Pi, and Cursor CLI behavior.
- A working Grok CLI runtime behind the observed wrapper.
- Model labels and tool inventories for non-Codex harness sessions.
- Real user-global installation, trust, update, rollback, or removal. Only
  isolated repository-contained lifecycle fixtures were exercised.
- Linux consumer lifecycle and hosted GitHub artifact attestations. The exact
  `go1.26.5` macOS release rehearsal passed separately in Phase 09.

All profiles therefore remain `provisional`.

## Risk

OpenCode, Kimi Code, Claude Code, and generic Agent Skills paths overlap in
some products. GDS must generate or install one intended projection per harness
profile and runtime-test duplicate-name behavior before any repository rollout.
No broad adapter rollout is eligible yet.

## Next dependency

C10 runs integrated assurance, scale, chaos, and clean multi-harness canaries.
No profile can be promoted and no legacy adapter authority can be retired until
its exact native runtime evidence passes.
