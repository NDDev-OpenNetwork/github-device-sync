# GDS plan/apply/verify v1 contract

Status: implemented for command-scoped local, Git, GitHub provider, harness,
workspace, repository, release-consumer, and recovery workflows. Every mutable
workflow uses an explicit immutable plan, a separately invoked apply, and an
evidence-backed verify; implementation support never bypasses runtime policy,
approval, or kill switches.

## Plan

A plan is a schema-valid immutable document containing:

- stable plan identity and operation;
- actor and exact repository scope;
- one precondition snapshot per scoped repository;
- ordered typed steps;
- per-step approval and compensation metadata;
- creation and expiry times;
- a canonical SHA-256 digest excluding only the digest field itself.

`gds validate plan --file <path>` validates a candidate without storing or
applying it.

## Apply order

The engine performs these gates in order:

1. load and revalidate immutable plan content and digest;
2. return an existing operation as an idempotent replay if the plan already
   started;
3. reject expired plans;
4. verify a detached Ed25519 approval bound to actor, validity window, exact
   plan ID/digest, approval class, and canonical scope digest;
5. prove that every action handler exists;
6. require a separately created active one-shot enablement for the same signed
   plan/device/session;
7. atomically compare-and-swap plan approval, consume enablement, create the operation and step
   journal, record approval evidence, and acquire the complete repository lock
   set in stable identity order;
8. observe and compare every precondition field;
9. call each handler and immediately verify its result;
10. atomically persist remaining step transitions, terminal operation and plan
   state, final journal events, and release the exact fenced lock set.

No handler is called when approval is absent, the plan expired, a lock is held,
state observation fails, or any precondition changed.

A lock conflict rolls the start transaction back, leaves the plan in `planned`
state, and creates no operation. Failure of any terminal write or exact lock
comparison rolls the terminal transaction back, leaves the operation
nonterminal, and retains all operation locks for explicit recovery.

## Preconditions

Each repository snapshot compares:

- HEAD OID;
- index tree OID;
- upstream OID;
- remote default OID;
- repository manifest digest;
- effective policy digest.

Any mismatch returns `GDS_STALE_PLAN`, blocks all pending steps, marks the plan
stale, and records the mismatch evidence before returning.

## Approval and enablement

`gds operation approve <plan-id>` writes a new mode-0600 signed approval JSON
from a local PKCS#8 Ed25519 key. `gds operation enable <plan-id>` independently
verifies that artifact against `GDS_TRUST_POLICY_FILE` and creates one active
local enablement bound to the exact approval digest, device, session, and plan.
Apply requires both and consumes the enablement in the same SQLite transaction
as journal creation and lock acquisition. Raw signatures are not journaled.

## Kill switches

Every apply, verify, and inspection result records all four effective switches:

```text
GDS_MUTATIONS_DISABLED
GDS_WEBHOOK_PROCESSING_READ_ONLY
GDS_ROLLOUT_PAUSED
GDS_HARNESS_HOOKS_DISABLED
```

Values must be exactly `true` or `false`. Invalid values fail closed. The
global mutation switch blocks before an operation, lock, checker, or handler is
started.

## Partial failure

Once a handler is called, any apply, verification, or durability failure is
treated as partial unless no mutation was attempted. Evidence
returned by the handler is preserved. Remaining steps are blocked. Automatic
compensation runs in reverse order only for earlier, fully verified steps whose
immutable plan declares an `automatic` action and explicit `reversible: true`
and `idempotent: true` proofs. It uses the same held fencing locks, invokes and
verifies a registered compensation handler, and journals every transition. The
currently failing step is never compensated because its partial effects are
unknown. Other modes still require a separately approved recovery plan.

## Verify

Explicit verification requires matching succeeded operation and plan states,
exactly one terminal success event, the complete succeeded step set, and no
remaining operation locks. It then dispatches each registered verification
handler and appends a verification event. It does not repeat apply.

## Inspect

`gds operation inspect <operation-id>` opens the state store query-only and
verifies the immutable plan identity, ordered step set, event identities, and
every event payload digest. It reports current locks, kill switches, and a
conservative recovery classification without changing state.

## Idempotency and recovery boundary

One plan can own at most one operation. Repeating apply returns that durable
operation and never calls the handler again. Failed, partial, blocked, and stale
plans require inspection and a new explicit recovery plan; blind
retry is not supported.

Each new step also receives an immutable idempotency key derived from the plan
digest and exact step content. Provider and Git handlers must inspect current
state before retrying and must never rely on a process-local retry counter.

## Recovery

```text
gds recover operation <operation-id> --plan
gds recover operation <operation-id> --apply <plan-id>
gds recover operation <operation-id> --verify <recovery-operation-id>
```

Recovery is a new journaled saga and never resumes a handler blindly. Its plan
binds the original operation/plan/step/event digest, exact lock identities and
fencing tokens, current repository HEAD, manifest, policy, current device, and
owner-process evidence. The recovery engine uses a separate
`operation-recovery` lock scope so it cannot steal the original repository
lock.

Automation is limited to expired locks owned by the current device whose PID is
proved absent. A pending-only interrupted operation may be closed as failed; an
operation with completed or failed steps may be closed as partial; an already
terminal operation may release its exact stale locks. The state transition,
pending-step closure, append-only recovery event, and exact stale-lock release
occur in one SQLite transaction. Active leases, live owners, other devices,
missing owner locks, and applying/compensating steps produce an explicit
manual-only recovery plan and no handler.

For every succeeded or failed original step, the decision also reports the
declared compensation mode and action. `explicit-plan` means a new separately
approved plan is required; `manual` remains manual-only; `none` is reported as
unavailable. Closing an interrupted journal never executes compensation.

## Registered surface

The canonical implemented capability states and their top-level command
carriers are owned by `core/capabilities`. The CLI registers commands in that
canonical order, `gds context` emits the same typed states, and an executable
contract test rejects documentation drift. Carriers identify commands that
contain the capability; their individual subcommands still perform exact
runtime, authorization, policy, and kill-switch checks.

<!-- gds-capability-registry:start -->
| Capability | Support | Runtime | Policy | Registered command carriers |
|---|---|---|---|---|
| `provider_observation` | `implemented` | `configuration-required` | `read-only` | `github`, `reconcile`, `repository` |
| `mutations` | `implemented` | `configuration-required` | `explicit-approval` | `complete`, `fork`, `generate`, `git`, `github`, `handoff`, `harness`, `memory`, `module`, `operation`, `portfolio`, `recover`, `release`, `repository`, `rollout`, `session`, `source`, `state`, `sync`, `workspace` |
<!-- gds-capability-registry:end -->

Handlers are registered per command and per immutable plan; there is no global
ambient handler registry. Current owner-facing workflows cover:

- repository anchors, generated projections, source verification baselines,
  estate registration, and conservative operation recovery;
- harness adapter materialize/update/rollback/remove lifecycles;
- workspace materialization and quarantine;
- bounded Git fast-forward, handoff, completion, gitlink pin, fork sync/detach,
  version-tag, and remote-update operations;
- GitHub governance, projected branch/content/draft-PR changes, and repository
  lifecycle/delete operations through exact-scope GitHub App runtimes;
- release materialize/activate/rollback/remove operations.

The `refs/gds/recovery/*` compare-and-swap handler remains an internal primitive
without standalone CLI registration. Generic shell/filesystem actions and any
action absent from the exact command plan remain unavailable. Provider and
network handlers additionally require private device-local runtime
configuration; mutation-mode policy, approval, scope, and kill switches are
rechecked before a handler is called.
