# ADR 0041: Installed harnesses and execution adapters

Status: Accepted

Date: 2026-10-08

Supersedes: ADR 0037 (the exact catalogue membership only)

## Context

Devin CLI is distributed by the public bootstrap, but GDS rejected its identity
as unknown. Registering an installed program must not imply that its skill,
instruction, hook, model or configuration behavior has passed adapter tests.

## Decision

- The canonical catalogue includes eight identities. Devin is provisional and
  supports bounded version observation; its runtime evidence is `not-proven`.
- The seven verified execution adapters remain in `WorkPolicyActiveIDs`.
  Selecting Devin for adapter synchronization reports `installed-paused`.
- The Devin module bridge maps to `macos-ubuntu-bootstrap` and explicitly
  declares `runtime_evidence: not-proven`. This mapping does not inherit the
  setup-systems runtime evidence owner.
- Default registry-wide validation retains the observation-only entry without
  promoting it. An explicit request for Devin runtime proof remains unproven.
- Adapter installation refuses unproven project skill paths. Version detection
  performs no model call and creates no vendor configuration.

## Verification

Synthetic version observation, empty-target adapter refusal, explicit runtime
proof refusal and registry/work-policy separation are exercised in Go tests.
The module bridge digest binds the per-mapping evidence policy.
