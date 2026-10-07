# Harness capability registry

`capability-registry.yaml` is the canonical index for eight harness identities.
Seven have execution adapters delivered by setup systems. Devin is registered
for bounded installed-version observation through the public bootstrap.

The execution/configuration allowlist remains the seven proven adapters.
Devin is provisional and cannot install a GDS configuration projection.
ADR 0041 records this separation; ADR 0037 retains the historical decision.

Canonical harness identities are:

<!-- generated:harness-ids -->
- `antigravity`;
- `claude-code`;
- `codex`;
- `cursor`;
- `devin`;
- `grok-build`;
- `opencode`;
- `pi`.
<!-- /generated:harness-ids -->

Harnesses with native AGENTS support consume the standalone generated
`AGENTS.md`; Claude Code receives a generated first-class
`.claude/CLAUDE.md` from the same typed repository and policy inputs. Skills
have one canonical source. Each adapter renders one native project-local path,
records an exact digest lock, and excludes destructive skills when the harness
has no proven explicit-only control.

`gds harness eval` emits the same twelve-case evidence schema for every
canonical identity. Deterministic lifecycle cases are distinct from actual
instruction discovery, invocation, triggers, hooks, and model output: missing
runtime evidence remains `NOT_PROVEN` and cannot promote a profile.
