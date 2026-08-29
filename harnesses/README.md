# Harness capability registry

`capability-registry.yaml` is the canonical index for the seven agent harnesses
this estate installs, one per setup system in `NDDev-OpenNetwork`. Each profile
records only current official documentation facts.

The catalogue and the work-policy allowlist are the same set, so nothing can be
catalogued but paused. ADR 0037 records why the catalogue is seven.

Canonical harness identities are:

<!-- generated:harness-ids -->
- `antigravity`;
- `claude-code`;
- `codex`;
- `cursor`;
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
