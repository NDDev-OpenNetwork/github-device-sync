# ADR 0037: Seven harnesses, one per setup system

Status: Accepted

Date: 2026-08-29

Supersedes: ADR 0011 (the seventeen-identity set only)

## Context

The catalogue carried seventeen harness identities. Seven are delivered by a
live setup system in `NDDev-OpenNetwork` — `antigravity-setup-system`,
`claude-setup-system`, `codex-setup-system`, `cursor-setup-system`,
`grok-setup-system`, `opencode-setup-system`, `pi-setup-system` — each released
and exercised. The other ten had no setup system, had been `provisional` since
they were added, and were mapped in `harnesses/module-bridge.yaml` to
repositories in the `nddev-*-app` line, all eighteen of which are archived
under the personal account.

So the catalogue described a world that no longer existed, and the bridge was
the record that said so: ten mappings naming archived repositories, kept
`active` because nothing forced them to be honest.

ADR 0036 corrected two identities. This record removes the ten that have no
delivery mechanism at all.

The catalogue and the work-policy allowlist were also different sizes — the
allowlist held seven, the catalogue seventeen — which is what
`GDS_DEVICE_HARNESS_PAUSED` existed to express: catalogued but on-pause. With
no unbacked identities left, that state has no members.

## Decision

1. The canonical harness set is exactly seven, one per setup system:
   `antigravity`, `claude-code`, `codex`, `cursor`, `grok-build`, `opencode`,
   `pi`.
2. `CanonicalIDs` and `WorkPolicyActiveIDs` are the same list. Nothing is
   catalogued but paused; an identity outside the set is rejected as unknown.
3. The module bridge carries seven `active` mappings and names no archived
   repository — not as a module, not as an alias. ADR 0036 kept the retired
   `nddev-*-app` names as `module_aliases` for provenance; that provenance now
   lives here and in Git history instead of in a live contract.
4. The ten removed identities lose their capability profiles, their runtime
   contract entries, and their source-register rows.
5. `zcode`'s runtime driver is removed with it. All seven remaining harnesses
   declare `runtime_evidence: delegated`, so the driver was not an evidence
   path; it was the last consumer of an identity that no longer exists.

## Consequences

- `harnesses/` holds seven profile directories.
- `core/cmd/gds-zcode-runtime-driver` and `core/harness/zcode_*.go` are
  removed. `gds-claude-runtime-driver` and `gds-codex-runtime-driver` remain,
  because their harnesses remain.
- Five source-register rows are removed — the ones governing only dropped
  harnesses. Rows governing a surviving harness are untouched.
- `GDS_DEVICE_HARNESS_PAUSED` becomes unreachable from the shipped
  configuration. The code path is retained: it is the correct answer if a
  future identity is ever catalogued ahead of its delivery.
- A device descriptor that selected one of the ten now gets
  `GDS_HARNESS_SELECTED_UNKNOWN`. Neither tracked device selects one.

## What is deliberately not removed

ADRs 0011, 0014, 0029, 0030 and 0035 still name the removed harnesses. They are
records of decisions taken when those harnesses existed, and rewriting them
would make the history unreadable rather than clean. ADR 0011's *live* claim —
"the canonical set is seventeen identities" — is corrected in place and points
here, because that sentence described current state rather than a past
decision.

The estate applies the same rule to repositories: archived repositories keep
their names and history, and are moved into the `archive` workspace rather than
deleted or renamed.

## Verification

- `go test ./...` — 64 packages, zero failures.
- `gds harness bridge validate`, `gds validate`, `gds doctor` — all exit 0.
- The catalogue, `harnesses/capability-registry.yaml` and
  `tests/harness/runtime-contract.yaml` agree exactly, which
  `TestCanonicalRegistryHasExactHarnessSet` enforces.
- No non-test reference to any removed identity remains in `core/`.

## Rollback

Revert as one commit. The removed profiles, driver and source rows are
recoverable from Git history; nothing was deleted that is not in the object
store.
