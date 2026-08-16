# ADR 0033: Return the control plane to a private repository

Status: Accepted

Date: 2026-07-27

## Context

This repository was published as public OSS on 2026-07-24, with an AGPL-3.0
licence, community health files, and a public security suite — OpenSSF
Scorecard, CodeQL, and Dependency Review, each calling the `public-*` variant of
the shared reusable workflow. No ADR recorded that move.

Publication was decided before the estate began recording device inventories.
A device descriptor now carries the observed checkout inventory (ADR 0032's
sibling change): the real repositories on a real machine, their provider slugs,
and their paths. That is an operational map of the owner's estate — which hosts
exist, which client work sits on which machine — and it is not material to
publish. The estate's own policy vocabulary has always separated
`visibility_contract` from `data_classification` precisely so this question can
be answered per repository; the answer for the control plane changed when its
content changed.

Nothing external depends on the public copy: zero forks, zero stars. The single
published release, `gds-v0.1.0`, is the owner's own artifact and stays reachable
to the owner.

Going private has a cost that must be stated rather than discovered. Actions
minutes are free for public repositories and billed for private ones, and this
repository runs twelve workflows. Three of them exist only because it was
public: OpenSSF Scorecard does not meaningfully run against a private
repository, and the CodeQL and Dependency Review callers use the `public-*`
reusables, which are the free-for-public variants.

## Decision

1. The repository's `visibility_contract` and `data_classification` become
   `private`.
2. The GitHub repository visibility becomes private.
3. `scorecard.yml`, `codeql.yml`, and `dependency-review.yml` are removed. They
   were the public-OSS suite; retaining them privately would bill for
   Scorecard's meaningless output and for scans the remaining suite already
   covers.
4. The AGPL-3.0 licence file stays. A licence grants rights to whoever receives
   the code; with no recipients it is inert, and removing it would discard the
   licensing intent rather than the distribution.
5. The remaining nine workflows stay, and their runs now bill.

## Consequences

- The device inventory can record real estate topology without publishing it.
- Actions minutes are now consumed by this repository. Under the current budget
  the working practice is `[skip ci]` on pushes with local verification through
  `scripts/validate_ci_tier.sh`, which runs the same gates.
- `AGENTS.md` and `.claude/CLAUDE.md` regenerate with `Visibility: private`,
  and the compiled policy reflects the private classification.
- The public security posture is reduced to what the private suite provides:
  gitleaks, OSV, Semgrep, actionlint, and zizmor. CodeQL coverage is lost until
  the budget supports a private run.
- Publishing again is a decision about content, not just a switch: the device
  inventories would have to move out of the repository first.

## Rollback

Set both classification fields back to `public`, restore the three workflow
files, and flip repository visibility. The device inventory must be removed
before that happens, or publication discloses the estate topology this decision
withheld.
