# ADR 0011: Version and runtime-test harness capabilities

Status: Accepted

Date: 2026-07-11

## Context

Harness instruction, skill, import, hook, and explicit-invocation behavior
changes over time. File presence does not prove runtime support.

## Decision

Each harness has a versioned capability profile with:

- product and tested version range;
- official source register entries;
- instruction discovery and limits;
- skill paths and projection rules;
- explicit-only mechanism;
- hook lifecycle;
- install/update/remove/doctor contract;
- runtime test evidence.

Canonical skill content uses the Agent Skills common denominator.
Harness-specific controls live in sidecars, settings, plugin manifests, or
generated projections.

The canonical set is seven identities: `antigravity`, `claude-code`, `codex`,
`cursor`, `grok-build`, `opencode`, and `pi` -- one per setup system.

Superseded by ADR 0037. This record originally named seventeen; the other ten
had no setup system, were held `provisional` and on-pause, and were removed
along with the retired `nddev-*-app` line they mapped to. The structure below
is unchanged: the enforced set still lives in `core/harness/registry.go`
(`CanonicalIDs`)
and is mirrored by `harnesses/capability-registry.yaml` and
`tests/harness/runtime-contract.yaml`; the three must agree exactly.

## Consequences

- Support claims expire and can become stale.
- Adapters remain reversible and independently testable.
- One canonical skill can target several harnesses without pretending their
  runtimes are identical.

## Alternatives considered

- One permanent capability matrix: rejected because products change.
- Copy separate skills manually for every harness: rejected because of drift.
- Lowest-common-denominator behavior only: rejected because safety controls such
  as explicit-only need adapter-specific projection.

## Verification

- Clean fixture install and remove.
- Root and nested instruction discovery.
- Exact skill discovery.
- Explicit invocation.
- Negative implicit invocation for destructive skills.
- Public/private projection and hook smoke tests.

## Rollback

Restore the previous verified adapter profile and projection bundle. Mark
unsupported or changed runtimes stale rather than silently degrading.
