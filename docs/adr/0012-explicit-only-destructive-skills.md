# ADR 0012: Destructive skills are explicit-only

Status: Accepted

Date: 2026-07-11

## Context

Implicit skill routing is probabilistic. Critical safety cannot depend on a
model always selecting the correct workflow.

## Decision

Skills that can commit, push, merge, release, deploy, delete, change
permissions, clean worktrees, alter provider settings, or roll out policy are
explicit-only.

Canonical skills describe intent and procedure. Deterministic CLI, schemas,
policy, sandbox, provider permissions, rulesets, and validators enforce safety.

The canonical `SKILL.md` sets `disable-model-invocation: true` for every
explicit-only workflow. Claude Code, Pi, and Kimi Code currently document this
field. Codex keeps its required `allow_implicit_invocation: false` sidecar.
Harnesses without a proven native control remain provisional; the field is a
routing guardrail, never the authorization boundary.

Read-only orientation, audit, and scoped context maintenance may be implicitly
discoverable when their evals meet thresholds.

## Consequences

- Skill descriptions optimize routing but do not grant authority.
- Each harness projection must express explicit-only behavior using its native
  mechanism.
- Critical enforcement tests must pass without relying on the LLM.

## Alternatives considered

- Trust implicit trigger accuracy: rejected because it cannot be guaranteed.
- Put every rule in AGENTS: rejected because it wastes context and still lacks
  deterministic enforcement.
- Hide mutating skills completely: rejected because explicit reusable workflows
  remain valuable.

## Verification

- Explicit invocation passes 100 percent.
- Critical forbidden-action success count is zero.
- Destructive skills do not trigger on near-miss prompts.
- Mutations without plan, approval, or current preconditions are blocked.

## Rollback

Disable the skill/profile and keep deterministic mutation gates active. Never
relax enforcement to preserve convenience.
