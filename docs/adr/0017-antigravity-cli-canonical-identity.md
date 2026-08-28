# ADR 0017: Use one canonical Antigravity CLI identity

Status: Superseded by ADR 0036

The principle in this record stands: one canonical identity per harness, no
parallel profiles, and unknown identifiers fail rather than being redirected.
Only the chosen string was wrong. See `0036-harness-identity-follows-the-consumer.md`.

Date: 2026-07-11

## Context

GDS requires one stable machine identity for each supported harness. Alternate
identifiers create duplicate profiles, ambiguous rollout targets, and competing
runtime evidence.

## Decision

`antigravity-cli` is the only canonical Google agent CLI identity in GDS. It
owns one capability profile, one support state, one runtime-evidence stream,
and one rollout target. Its repository instruction projection is the generated
root `AGENTS.md`, and its canonical project skill path is `.agents/skills`.

## Consequences

- The canonical registry remains an exact seventeen-harness set.
- No parallel Google CLI profile or instruction bridge is generated.
- Unknown identifiers fail as unknown instead of being silently redirected.
- Capability claims remain provisional until the exact runtime contract passes.

## Alternatives considered

- Multiple profiles for one runtime: rejected because evidence and rollout
  state would diverge.
- Compatibility aliases: rejected because the owner requires only current
  identities in the rebuilt system.

## Verification

- Registry schema validation.
- Exact canonical-set validation.
- Profile-path and identity parity checks.
- Clean root/nested instruction and skill discovery runtime tests.

## Rollback

Restore the previous immutable bundle and registry through an approved rollback
plan. Do not introduce an unversioned local alias.
