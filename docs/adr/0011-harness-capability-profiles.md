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

The owner-selected canonical set is seventeen identities:
`antigravity`, `claude-code`, `cline`, `codex`, `cursor`,
`github-copilot-cli`, `grok-build`, `junie-cli`, `kilo-cli`, `kimicode`,
`kiro-cli`, `mimocode`, `opencode`, `pi`, `qoder-cli`, `qwen-code`, and
`zcode`. The enforced set lives in `core/harness/registry.go` (`CanonicalIDs`)
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
