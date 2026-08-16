# ADR 0027: Give a submodule-consumed repository no standalone checkout

Status: Accepted

Supersedes: the reusable-module dual-placement clause in ADR 0018

Date: 2026-07-26

## Context

ADR 0018 separated logical portfolio classification from device placement and
made both standalone checkouts and embedded submodules first-class, audited
independently. Nothing forbade a repository from being materialized both ways at
once, and in practice several were: `ci-workflows` and `macos-ubuntu-bootstrap`
existed as gitlinks under the control plane *and* as standalone checkouts in the
`nddev` root, and the three harness apps existed as gitlinks under
`example-harnesses` *and* standalone.

Every such pair made one stable repository ID resolve to two distinct Git
stores, which layout analysis rejects: twelve findings, six
`GDS_CONTEXT_IDENTITY_CONFLICT` and six `GDS_IDENTITY_INDEX_ID_CONFLICT`, held
`gds workspace audit` in `blocked`. The two stores also diverged in practice —
three of the standalone checkouts carried commits that existed nowhere else.

These repositories have their own remotes and their own release lifecycle, but
they are developed inside their consuming superproject. A second checkout adds
no capability and guarantees drift.

## Decision

1. A repository consumed through a `git-submodule-consumer` relationship is
   materialized only as the superproject's gitlink. It gets no standalone
   checkout on any device.
2. Its checkout path is inherited from the Git-reported superproject, per ADR
   0018 point 7. Device selectors do not place it.
3. Embedded placement requires roles on both sides: the superproject declares
   role `superproject` and the module declares role `module`. Declaring role
   `module` obliges a `module` block in `.gds/repository.yaml`, enforced by
   `schemas/v1/repository.schema.json`.
4. Before removing a redundant standalone checkout, its commits must be
   preserved into the submodule's own Git store. Deleting a checkout that holds
   unreachable commits is data loss, not cleanup.

## Consequences

- All twelve identity findings are cleared and audit leaves `blocked`.
- There is exactly one working tree per repository ID on a device, so "which
  copy is current" stops being a question.
- Work on a module happens inside the superproject checkout, on the module's own
  branch, and is published through the module's own remote before the gitlink
  advances.
- A device root can no longer be read as an inventory of every repository the
  estate knows: submodule-consumed repositories are absent from it by design.

## Rollback

Re-clone the standalone checkouts and restore the removed roles. Nothing in the
provider is involved. Rollback re-introduces the identity findings, which is why
it should only follow a deliberate change to the placement model.
