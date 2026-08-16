# C8 GitHub governance mutation evidence

Status: C8 provider-write surface implemented and verified locally; live App
installation, credentials, permissions, and GitHub writes remain `NOT_PROVEN`.

Date: 2026-07-11

This is historical evidence for the original fail-closed foundation. The
controlled-mutation rollout approved on 2026-08-10 supersedes the estate
posture described here; it does not retroactively turn these fixture results
into live GitHub evidence.

## Completed locally

- Added a stable governance evidence digest covering repository identity and
  lifecycle, merge and security state, Actions policy, selected Actions,
  workflow-token policy, and bounded rulesets while excluding volatile request
  metadata.
- Added four typed, repository-bound operation handlers for merge settings,
  Actions permissions, selected Actions, and workflow-token permissions.
- Added exact field and full-state checks immediately before and after every
  provider write, idempotent desired-state detection, typed redacted evidence,
  and zero handler retries.
- Added a deterministic remediation compiler that chains exact expected and
  desired evidence digests across operation steps.
- Added the official GitHub selected-actions discovery barrier. When the
  current policy is not `selected`, the first plan changes only the Actions
  policy, observes the newly available selected-actions state, and requires a
  new plan.
- Added CLI `--plan`, `--apply`, and `--verify` modes. Apply loads the separate
  Mutation App runtime only after canonical management, lifecycle, operation,
  and estate mutation-mode gates pass.
- Proved that the then-current `observe-only` / `mutation_mode: disabled` estate
  blocks apply before the missing mutation runtime is inspected.
- Added repository-bound branch, content, workflow-caller, and draft-PR
  handlers. They enforce exact base and head OIDs, expected blob state,
  base-relative file status, bounded content digests, one forward-only change
  set, and one exact draft PR with no blind retries.
- Added `gds github projection-pr --plan|--apply|--verify`. It compiles the
  current generated projection, verifies immutable provider identity and
  default branch, derives one deterministic GDS branch, excludes files already
  equal on base, and stores provider plus local preconditions in the durable
  operation journal.
- Existing GDS branches are reusable only when their merge base is the exact
  planned base, they are not behind, and every changed path belongs to the
  generated projection. Idempotent replanning preserves `added` versus
  `modified` relative to base even after desired content already exists on the
  GDS branch.
- Added exact repository lifecycle and deletion handlers. Rename and archive
  bind provider state before local remotes or anchors can change; delete is a
  separate exact-confirmation operation. Transfer is deliberately blocked
  because the official transfer endpoint does not accept installation tokens
  and completes asynchronously.
- Added closed custom-property and default-branch ruleset handlers. Property
  updates preserve unrelated values and clear removed managed values
  explicitly. Ruleset writes require privileged Mutation-App observation and
  reject hidden or non-empty bypass actors.
- Generated thin Actions callers use one reusable workflow implementation,
  an immutable full commit SHA, stable job/check name, explicit read-only job
  permission, empty top-level permissions, and reject `pull_request_target`,
  inherited secrets, mutable refs, and write-all permissions.

## Evidence

```text
python3 scripts/validate_gds_schemas.py
GDS schema validation: PASS

GOTOOLCHAIN=go1.26.5 go test ./...
PASS

GOTOOLCHAIN=go1.26.5 go vet ./...
PASS
```

The governance fixture executes four sequential settings steps through plan,
approval, apply, durable journaling, and verify. Projection fixtures execute a
multi-file branch/content/draft-PR saga and its final comparison. Separate
fixtures change provider state, add an unexpected branch path, hide ruleset
bypass actors, or alter custom properties and prove zero provider writes.

## External evidence still required

- Create/install separately scoped Inventory and Mutation Apps only after an
  exact approved provider plan.
- Verify effective permissions, selected-repository scope, request IDs, rate
  behavior, and every live write against canary repositories.
- At this evidence date, keep `mutation_mode: disabled` until C9-C10 release
  and assurance gates pass and a C11 canary plan receives explicit approval.
- No App installation, permission change, selected repository grant, branch,
  content update, PR, ruleset, custom-property change, or other live provider
  write was performed by this phase.
