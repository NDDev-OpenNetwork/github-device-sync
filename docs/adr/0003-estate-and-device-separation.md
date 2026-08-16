# ADR 0003: Separate estate identity from device deployment

Status: Accepted

Date: 2026-07-11

## Context

The legacy physical hierarchy treats the device root as a logical repository
parent. Repositories may exist on several devices, in several worktrees, or not
be materialized locally.

## Decision

Estate is the complete managed graph. Device is a deployment target with desired
materialization and observed local state.

Repository identity, ownership, role, and relationships do not depend on:

- device;
- local path;
- current branch;
- checkout presence.

Portable desired device configuration uses variables. Device-specific paths,
observations, locks, and journals live under the local state directory.

## Consequences

- One repository can be represented on multiple devices without identity
  duplication.
- Missing checkout is a normal observed state.
- Workspace materialization becomes query/profile-based.
- Current device snapshots require migration into desired and observed
  components.

## Alternatives considered

- Keep device-root as universal parent: rejected because it conflates identity
  and materialization.
- Commit current checkout lists: rejected because they are mutable device state.

## Verification

- The same repository ID resolves on two device fixtures with different paths.
- A repository can be absent locally without disappearing from estate inventory.
- No absolute local path appears in portable desired configuration.

## Rollback

Keep the legacy device snapshot reader available until target device/context
fixtures pass. Do not auto-materialize missing repositories during rollback.
