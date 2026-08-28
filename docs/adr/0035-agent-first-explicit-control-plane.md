# ADR 0035: Agent-first explicit control plane and evidence-bound mutation

Status: Accepted

Date: 2026-08-09

## Context

ADR 0009 established plan/apply/verify, but later implementation still admitted
string approval references, whole-object GitHub ruleset replacement, delegated
harness declarations without exact release evidence, environment-relaxed
performance gates, and device intent as a proxy for current device truth.

## Decision

- GDS is an internal NDDev agent-first control plane, not an autonomous desired-
  state reconciler. Declared/live conflict creates drift and invalidates a plan.
- Mutation requires an immutable plan, signed exact-plan approval, local one-shot
  enablement, fresh CAS preconditions, declared write-set locks/fencing, ordered
  apply, verification, and durable evidence.
- GitHub ruleset ownership is partial. GDS owns enforcement and required checks;
  bypass, conditions, review/merge semantics, and unknown fields are preserved.
  Existing resources enter ownership only through an observation-only adoption
  plan.
- Required check contexts are generated from an allowlisted security workflow
  policy, exact caller pins, and content-digested reusable workflow facts.
- The harness catalogue remains seventeen identities. Work-policy active is
  exactly antigravity-cli, claude-code, codex, cursor-cli, grok-build, opencode,
  and pi. Every harness emits
  isolated signed exact-version evidence; a separately signed manifest binds the
  aggregate. Canary may be provisional and never auto-promotes. Stable/frozen
  require all seven.
- Device operational truth is SQLite plus signed compact evidence. Read paths are
  offline by default and report claim-specific freshness; apply re-reads.
- Cross-repository plans are dependency DAGs. Automatic compensation is valid
  only when both reversible and idempotent are explicitly proven.
- Performance modes are explicit: deterministic-required, relative-required,
  absolute-calibrated, and informational. Ambient `CI=true` never weakens a gate.
- The local journal remains recovery authority. Structured OTLP export uses a
  bounded SQLite outbox; OpenObserve failure cannot change operation outcome.

## Consequences

Mutation callers must migrate from a string reference to a signed approval JSON
file and configure a public trust policy. State schema v8 adds plan enablement,
device evidence, and telemetry outbox tables. Plan v1 now emits declared write
sets; old durable plans without them require re-planning rather than migration.
Stable/frozen release construction is unavailable until a signed active-seven
evidence directory and trust policy are supplied.

`zcode` remains catalogued and may remain installed as `installed-paused`, but it
cannot be selected by active work policy. Device descriptor changes require a
real observation and explicit plan; no automatic uninstall is implied.

## Verification

The contracts are exercised by the trust/approval, state enablement, ruleset
preservation, harness evidence, freshness/device evidence, operation concurrency,
portfolio DAG, assurance, telemetry outbox, release builder, schema, and workflow
policy test suites.

## Rollback

Rollback requires a new signed plan. State v8 evidence remains readable and must
not be discarded. Reverting partial ruleset ownership must not issue a provider
write because an older whole-object writer cannot safely preserve external data.
