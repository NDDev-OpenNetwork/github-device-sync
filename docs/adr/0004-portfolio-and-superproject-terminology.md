# ADR 0004: Distinguish portfolios, monorepos, and superprojects

Status: Accepted

Date: 2026-07-11

## Context

The current direct metadata repositories are named monorepo but do not track
project code in one Git history. Some actual projects contain submodules.

## Decision

- Portfolio is a logical group of independent repositories.
- Monorepo is one Git history that directly tracks multiple projects or
  packages.
- Superproject is a Git repository that pins submodules through gitlinks.
- A repository may have portfolio-registry and superproject roles
  simultaneously.

ADR 0018 resolves the former open migration choice: the direct metadata
repositories are retired, and device workspace roots materialize portfolio
members without becoming Git boundaries.

## Consequences

- Portfolio-wide work becomes one aggregate plan with one transaction per
  repository.
- Git boundaries are no longer inferred from a directory label.
- Existing repository names may remain temporarily without controlling machine
  semantics.

## Alternatives considered

- Rename everything immediately: rejected because cosmetic churn does not
  establish behavior.
- Continue calling every collection a monorepo: rejected because it creates
  incorrect branch and transaction assumptions.

## Verification

- Schema roles distinguish portfolio membership from git-submodule
  relationships.
- A portfolio change fixture produces independent repository subplans.
- A true monorepo fixture remains a single Git boundary.

## Rollback

Terminology rollback is documentation-only until schemas are activated. Existing
repository names and paths remain unchanged.
