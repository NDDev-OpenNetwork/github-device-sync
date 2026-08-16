# GDS bundle rollout v1 contract

Status: deterministic planning, gates, and local durable state implemented;
repository PR creation and external rollout are disabled.

## Planning request

`schemas/v1/rollout-request.schema.json` describes an exact, side-effect-free
request with a release envelope reference, stable repository IDs, cumulative
ring policy, and failure gate.

```text
gds rollout plan --file <request.yaml>
```

The planner accepts at most 2000 unique repositories. Target order in the
request has no effect. It ranks targets deterministically from rollout ID plus
repository ID, allocates cumulative waves, sorts each emitted wave, binds the
complete target set by digest, and produces an immutable plan digest.

The default tested shape is:

```text
canary:          first 5 targets
representative: cumulative 1%
early:          cumulative 10%
general:        cumulative 100%
```

At 2000 targets this produces independent waves of 5, 15, 180, and 1800.

## Gates

A later wave cannot advance until every target in the current wave has a final
result. Any security failure or required-check failure pauses the rollout.
Failure rate must not exceed the plan's explicit threshold. Missing evidence is
`wait`/`NOT_PROVEN`, never success.

## Durable state

State schema v3 stores:

- immutable rollout plans;
- one target row per repository and wave;
- compare-and-swap rollout and target transitions;
- append-only rollout events;
- bundle acceptance and anti-rollback evidence.

The database refuses wave advance or completion while current-wave targets are
not all successful. Rollout plan content and event history cannot be updated or
deleted.

## Mutation boundary

The current implementation creates no branch, commit, pull request, merge,
tag, release, deployment, or provider setting. Those adapters require a later
approved plan/apply/verify operation and current provider precondition checks.
