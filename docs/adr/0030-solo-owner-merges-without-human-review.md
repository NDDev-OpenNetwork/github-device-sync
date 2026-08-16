# ADR 0030: Let the sole owner's agent merge and clean up without a second reviewer

Status: Accepted

Date: 2026-07-26

## Context

`macos-ubuntu-bootstrap` required one approving code-owner review on `main`.
Every other estate repository already required zero: `ci-workflows` via classic
branch protection, `example-harnesses` and `nddev-zcode-app` via their `pull_request`
ruleset parameters, the rest by having no branch rule at all.

That single requirement was unsatisfiable in practice. This estate has exactly
one developer, who is also the author of every pull request. GitHub does not let
an author approve their own pull request, so a required approval count of one
means the only person who could approve is the only person who cannot. Work
landed nowhere and branches accumulated.

The requirement also bought nothing this estate does not already have. What
actually gates a merge here is the required status checks — for
`macos-ubuntu-bootstrap` nine of them, covering script validation, dependency
pins, pytest, cross-platform smoke, secret scanning, SAST, OSV, workflow lint,
and dependency review. Those run on the change itself and cannot be waved
through. A self-approval, had GitHub permitted one, would have proven nothing.

The estate posture that matters is unchanged and is enforced elsewhere:
`security.external_write_requires_approval` stays `true`, every mutating GDS
operation stays plan → apply with an explicit approval reference, and
`workflow.can_approve_pull_request_reviews` stays `false` so `GITHUB_TOKEN`
cannot approve anything.

## Decision

1. No estate repository requires an approving pull-request review on its default
   branch. `macos-ubuntu-bootstrap`'s `required_pull_request_reviews` is removed;
   no other repository needs a change.
2. Required status checks remain the merge gate and are not reduced. A pull
   request merges when its checks pass, and not before.
3. The owner's agent is authorized to merge a pull request it opened, once the
   checks pass, and to delete the head branch in the same action. It is not
   authorized to bypass a failing or missing required check — `--admin` is not a
   remedy for red CI.
4. Merged branches are deleted immediately. A branch left behind after its merge
   is drift, not history: the commits live on the default branch.
5. Signed commits, linear history, and the pull-request requirement itself stay.
   The change removes a human approver, not the transaction.

## Consequences

- Pull requests stop stalling on an approval that cannot exist, and branch lists
  stay short enough to read.
- The merge decision now rests entirely on automated evidence, which raises the
  value of each required check. A check that is advisory in intent but marked
  required, or required but never reported, blocks work permanently — that is
  now the only failure mode, and it is a configuration bug to fix at the source.
- Adding a second developer later means restoring a review requirement. This ADR
  is the record of why it was removed, so that restoration is a deliberate act
  rather than a rediscovery.
- Nothing about GDS operation approval changes. An apply still needs its exact
  approval reference; this ADR governs provider-side pull requests only.

## Rollback

Restore `required_pull_request_reviews` on the affected repository with
`required_approving_review_count: 1`, and stop authorizing agent self-merge in
`skills/canonical/gds-complete-work/SKILL.md`.
