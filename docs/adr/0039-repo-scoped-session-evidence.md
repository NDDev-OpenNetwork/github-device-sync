# ADR 0039: Repo-scoped session evidence

Status: Accepted

Date: 2026-09-21

## Context

GDS already produces device-wide evidence (`core/deviceevidence`): a signed
inventory of an entire workspace — every repository, every harness, provider
refresh digests. That is the wrong unit for the recurring question "what did
this agent session touch". A session runs inside one repository boundary,
which may itself contain nested Git modules; sweeping `~/Developer` records
sibling repositories the session never entered and leaks their state into an
artifact whose subject is narrower.

External practice for agent audit trails converged on the same invariants:
capture repository state at the session boundary, record typed bounded
observations, canonicalize the signed payload, separate the signature domain,
and state honestly what the artifact does not prove (in-toto-style separation
of subject, predicate and envelope; the Agent Receipts specification reaches
the same shape).

## Decision

`gds evidence record` produces one signed artifact scoped to exactly the
repository it runs in — and to every Git module inside that boundary, because
a submodule is part of the repository's own tree.

1. **Scope is the repository boundary.** The artifact carries the
   `.gds/repository.yaml` identity, HEAD mode and OID, branch and upstream
   position, staged/unstaged/untracked/conflicted counts, the sorted changed
   path list, and every submodule's path, gitlink OID and checked-out OID.
   Nothing outside the worktree root is read.
2. **The artifact is private.** It names working-tree paths, so it is written
   under the device state root (`session-evidence/<repo>/<id>.json`, mode
   `0600`) — never committed to the repository it describes.
3. **Integrity is canonical + signed.** `evidence_digest` is the canonical
   JSON digest of the payload; the signature is Ed25519 over the
   `gds-session-evidence/v1` domain under the `session-evidence` trust role.
   Each record links the newest prior artifact for the same repository via
   `previous_evidence_digest`, forming a local hash chain.
4. **Verification is independent.** `gds evidence verify` needs only the
   artifact and a trust policy — not the session, the harness, or the
   repository. It checks schema, digest, signature and structural invariants
   (required identity fields, sorted paths, unique submodule paths).
5. **Honesty about claims.** The artifact proves that a signing identity
   recorded this repository state at this time under this session identity.
   It does not prove the named session produced that state, that provider
   transcripts were captured, or that the session's work was correct.
   Pre-existing dirty state is recorded as observed, not attributed.

## Consequences

- Historical Cursor, Claude Code, Grok and Codex sessions cannot be
  retroactively attested; provider transcript content is outside this
  artifact's scope unless a harness exposes it, in which case it belongs in a
  separate harness evidence document, not here.
- Estates grant the `session-evidence` role to the identities allowed to
  attest agent sessions (device owners), keeping the signing authority off
  the release path exactly as ADR 0038 required.
- A later end-of-session or diff-bound receipt can extend the same schema
  family; the hash chain already orders multiple records per repository.
