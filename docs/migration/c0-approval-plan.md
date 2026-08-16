# C0 execution approval record

Status: approved; execution in progress

Date: 2026-07-11

## A1 — Exact Go validation toolchain

Status: completed

The approved command used the official cached Go `1.26.5` toolchain without
changing the global Go installation:

```text
GOTOOLCHAIN=go1.26.5 GDS_RELEASE_GO_VERSION=go1.26.5 \
  scripts/validate_go_core.sh
```

Result: full validation passed, including module integrity, format, vet, unit,
integration, race, canonical validators, generation, and cross-builds.

## A2 — Preservation, topology cutover, and publication

Status: approved; execution in progress

The owner approved the recommended complete redesign, correct placement of all
local repositories, retirement of unnecessary metadata repositories, atomic
commits, push, and hosted verification.

Execution authority and order:

1. Preserve dirty direct metadata repository work on
   `archive/gds-pre-c0-context-20260711`, push it, and verify the remote OID.
2. Apply the exact device-workspace move plan in
   `device-workspace-cutover-plan.md` with per-repository verification.
3. Create `feat/gds-control-plane` from the verified root main OID while
   preserving the existing worktree.
4. Retire the legacy metadata gitlinks and `.gitmodules` from active topology.
5. Split the root migration into dependency-safe Conventional Commits.
6. Push the feature branch, create one draft PR, and verify hosted checks on
   the exact remote OID.

The direct metadata GitHub repositories remain available as rollback evidence.
Deleting or changing their GitHub settings is not part of this approval record
and requires a later exact provider plan.

## Still excluded

- merge or auto-merge;
- force push or history rewrite;
- deletion of remote repositories;
- GitHub App, ruleset, permission, webhook, or visibility changes;
- release, tag, deployment, or broad estate rollout;
- unplanned harness installation or system configuration mutation.
