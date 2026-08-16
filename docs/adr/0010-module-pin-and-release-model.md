# ADR 0010: Separate module consumption, pinning, and publication

Status: Accepted

Date: 2026-07-11

## Context

A module can be consumed as a submodule, package, vendored source, or service. A
Git commit pin does not always require a GitHub Release.

## Decision

Model three independent dimensions:

- consumption type;
- pin policy;
- publication policy.

Supported initial pin policies:

- default-branch commit;
- immutable version tag;
- package version.

The superproject gitlink remains authoritative for the pinned submodule commit.
GDS adds identity, consumer graph, policy eligibility, publication proof, and
compatibility checks.

## Consequences

- Public modules can serve multiple consumers.
- Release requirements match actual distribution contracts.
- Consumer updates follow dependency order.

## Alternatives considered

- Require GitHub Release for every submodule commit: rejected as unnecessary.
- One continuous/versioned/package enum: rejected because it conflates
  independent dimensions.
- Accept any pushed commit: rejected because final-ref and policy eligibility
  matter.

## Verification

- Fixtures cover all consumption and pin modes.
- An unpublished or policy-ineligible pin blocks consumer completion.
- Temporary task pins cannot remain on consumer main.

## Rollback

Restore the previous verified pin through a new consumer change. Published
immutable versions are corrected with a new release, not retagged.
