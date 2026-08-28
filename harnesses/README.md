# Harness capability registry

`capability-registry.yaml` is the canonical index for the seventeen owner-selected
agent harnesses. Each profile records only current official documentation facts
and remains `provisional` until an exact installed version passes the clean
runtime contract suite.

Canonical harness identities are:

- `antigravity`;
- `claude-code`;
- `cline`;
- `codex`;
- `cursor`;
- `github-copilot-cli`;
- `grok-build`;
- `junie-cli`;
- `kilo-cli`;
- `kimicode`;
- `kiro-cli`;
- `mimocode`;
- `opencode`;
- `pi`;
- `qoder-cli`;
- `qwen-code`;
- `zcode`.

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
