# ADR 0036: Harness identity follows the consumer contract

Status: Accepted

Date: 2026-08-29

Supersedes: ADR 0017

## Context

ADR 0017 established that each harness has exactly one canonical machine
identity in GDS, that no parallel profile is generated for the same runtime,
and that an unknown identifier fails rather than being silently redirected.
That principle is correct and is retained in full.

What it got wrong was the value. It named `antigravity-cli` as the canonical
Google agent CLI identity, and the registry later recorded `antigravity` — the
value the actual consumer uses — as a *legacy alias* of it. The relationship
was inverted, and `core/harness/codex_test.go` asserted the inversion, so a
test was holding the wrong answer in place.

GDS is not the owner of harness identity. `ai-stp` is. It declares the closed
set in `packages/foundation/src/ai_stp_foundation/harnesses.py` as a
`HarnessId` literal, derives the list from it with `get_args` rather than
restating it, and publishes that set to every agent through `capabilities`.
The governing records there are ADR-0003, ADR-0033, ADR-0120 and
SPEC-001 REQ-105. The canonical set is:

`claude-code`, `codex`, `pi`, `opencode`, `grok-build`, `cursor`,
`antigravity`.

Neither `-cli` suffix ever named anything outside GDS. The Antigravity profile
detects the binary `agy`, and the real Cursor CLI binary is `cursor-agent`.
The suffixes were derived from the archived NDDev module names
`nddev-antigravity-cli-app` and `nddev-cursor-cli-app` — an identity copied
from a source that was later retired, with the copy left asserting.

## Decision

The canonical GDS harness identity is the `ai-stp` `HarnessId` value.
`antigravity-cli` becomes `antigravity` and `cursor-cli` becomes `cursor`
across the registry, capability profiles and their directories, the module
bridge, the delegated-evidence and runtime-manifest schema enums, the release
evidence archive members, and the canonical and work-policy identity lists.

Where GDS and the consumer disagree about a harness identity in future, the
consumer wins, and GDS records the change rather than negotiating it.

The previous strings are retained in `legacy_aliases` and in the harness
profile `aliases`. This does not reopen the compatibility-alias question ADR
0017 closed: an alias is migration provenance and collision-detection input,
never a second live identity. Unknown identifiers still fail as unknown.

## Consequences

- The canonical registry remains an exact seventeen-harness set.
- Release evidence archive members are renamed `antigravity.json` and
  `cursor.json`. Two parties are involved and they are not the same one:
  `NDDev-it-com/setup-systems` owns the harness *runtime* evidence and is what
  `evidence_owner` names, while the signed archive GDS consumes is produced and
  signed in the private estate by
  `github-device-sync-estate/scripts/produce-harness-evidence.py`. That script
  and its test `tests/test_harness_evidence_producer.py` carry the active-seven
  list and must land the same rename. GDS ships first; the estate then advances
  its pin and its producer together, so no intermediate state is broken.
- The two schema enums are narrowed to the new values. Nothing outside GDS
  consumes them: the setup-systems repository was grepped for
  `capability-registry`, the profile paths and `module-bridge` and has no hits,
  and its only GDS surfaces are `.gds/repository.yaml` and
  `gds validate repository`.
- Capability profile directories move to `harnesses/antigravity/` and
  `harnesses/cursor/`.

## Alternatives considered

- Keep `antigravity-cli` and ask `ai-stp` to change: rejected. GDS consumes
  the identity and does not define it, and the consumer's set is derived from
  a single literal specifically so it cannot drift.
- Accept both values as live identities: rejected for the reason ADR 0017 gave
  — evidence and rollout state would diverge.
- Leave it and document the mismatch: rejected. A join on harness ID would
  silently drop two of seven and report five as though that were the answer.

## Verification

- `gds harness bridge validate --gds-root <root>` passes, which covers
  catalogue coverage, identity and alias collisions, and lifecycle rules.
- Registry and schema validation, and the full Go test suite.
- The alias assertion in `core/harness/codex_test.go` now checks both
  directions of the corrected mapping.

## Rollback

Revert this change as one commit and restore the estate producer's previous
member names in the same operation. Do not roll back only one of the two: the
archive member list is an exact filename match, so a half-rollback fails the
release evidence check rather than degrading.
