# ADR 0005: Use stable identities and typed relationships

Status: Accepted

Date: 2026-07-11

## Context

A universal parent field cannot represent shared modules, forks, multi-device
checkouts, worktrees, and context inheritance without ambiguity.

## Decision

Every managed object receives a stable GDS identity independent of provider
locator and filesystem path.

Relationships are explicit typed edges, including:

- portfolio membership;
- fork-of;
- generated-from;
- git-submodule consumer;
- package consumer;
- device checkout;
- worktree;
- embedded context source.

`generated-from` is declared on the rendered repository and names the repository
that renders it. It is not a dependency edge: nothing is vendored, pinned or
executed from the generator, so it derives no consumer and requires no
`module.consumption` declaration. Declaring it in the other direction was
rejected for the same reason a parent field was: one generator would carry a
list of every tree it renders, and that list is a second copy of a fact the
renderer already owns.

The direction also decides whether the fact can be checked. A rendered tree
asserting "I came from X" can be verified against the generator's own output; a
generator asserting "I produce these seven" has nothing local to check it
against, and drifts exactly when a tree is added or removed.

Provider owner/name and local paths are mutable locators with alias/history
records.

## Consequences

- Rename and transfer do not break relationships.
- Shared modules can have several consumers.
- Resolution and validation become more explicit.
- Schemas and indexes must reject ambiguous or unknown relationship types.

## Alternatives considered

- One parent field: rejected as ambiguous.
- Filesystem path as identity: rejected as device-specific and mutable.
- GitHub owner/name as identity: rejected because rename and transfer are valid.

## Verification

- Rename/transfer fixtures preserve GDS IDs and relationships.
- Shared-module fixtures resolve multiple consumers.
- Unknown relationship types fail schema validation.

## Rollback

Retain an alias map and original provider IDs. Schema migration must round-trip
legacy locators without data loss before target IDs become authoritative.
