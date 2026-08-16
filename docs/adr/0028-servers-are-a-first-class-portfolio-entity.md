# ADR 0028: Make servers a first-class portfolio entity

Status: Accepted

Date: 2026-07-26

## Context

ADR 0026 merged the two per-owner server portfolios into one `portfolio:servers`
so every server checkout resolves to one flat workspace root. That fixed
placement but left servers without an identity of their own in the policy model:
the two server selectors still assigned the generic owner profiles
(`organization-default`, `personal-default`), so a server repository was
governed exactly like any other repository of its owner.

Forks already have such an identity — `policies/portfolios/fork-default.yaml`
is a `portfolio` tier policy matching both fork portfolios, and the fork
selectors assign `repository-default` + `fork-default` rather than an owner
profile. Servers had no equivalent.

Servers are a distinct kind of thing. The estate currently holds seven of them,
all owned by `example-org` and all private, and each one is a deployment host
whose checkout contains further project checkouts rather than a product of its
own. Their governance concerns — how changes reach a live host — are not the
concerns of an ordinary source repository.

A single owner-agnostic `servers` selector is not expressible:
`schemas/v1/selector.schema.json` requires `match.owner`. Two owner-scoped
selectors feeding one shared portfolio is therefore the exact shape the model
allows, and it keeps the `server-`-prefixed fork exclusion from ADR 0026 intact.

## Decision

1. `policies/portfolios/servers-default.yaml` declares the servers entity at the
   `portfolio` tier, matching `portfolio:servers`, mirroring `fork-default`.
2. Both server selectors assign `repository-default` + `servers-default` and no
   longer assign an owner profile, matching how fork selectors are wired.
3. The policy declares `distribution: private`. Server repositories are private,
   and `core/compiler` only compiles a policy profile for a repository whose
   visibility rank permits its distribution, so this makes it structurally
   impossible for a public repository to pick up server governance.
4. It applies `git.integration: pull-request`, `git.branch_cleanup: merged-only`
   and `rollout.mode: pull-request`, preserving the pull-request posture the
   owner profiles previously supplied.

## Consequences

- Servers are now visible in the model as their own class, at the same tier as
  forks, instead of being ordinary owner-scoped repositories that happen to
  share a workspace root.
- Server policy can evolve without touching `organization-default` or
  `personal-default`, which govern unrelated repositories.
- The `personal-servers` selector currently matches nothing — every server is
  org-owned. It is retained deliberately so a personal server is classified
  correctly the day one appears, rather than silently falling into
  `portfolio:personal-projects`.
- A public repository can never compile `servers-default`; if a server is ever
  made public, its policy stops applying and the compiler says so rather than
  degrading quietly.

## Rollback

Delete `policies/portfolios/servers-default.yaml` and restore the owner profiles
in the two server selectors. No provider or device state is involved.
