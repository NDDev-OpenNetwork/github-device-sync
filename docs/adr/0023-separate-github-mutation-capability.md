# ADR 0023: Separate GitHub mutation capability and repository-bound writes

Status: Accepted

Date: 2026-07-11

## Context

The Inventory App must remain usable by a permanent observe-only controller.
Adding write permissions to it would turn every read worker and webhook path
into a write-capable process. A generic write client would also allow an agent
or bug to substitute another repository path after approval.

## Decision

- Inventory and Mutation Apps use separate canonical capability documents,
  runtime files, App IDs, provider installation IDs, and secret locators.
- Mutation App installation selection is `selected`; `all` is invalid.
- The write permission map is exact and closed. Extra or stronger permissions
  are rejected, not tolerated.
- A mutation factory is bound to one immutable repository ID, verified
  owner/name locator, and an operation subset before write methods are exposed.
- Force updates, auto-merge, visibility changes, permission changes, and
  ruleset bypass are not ordinary methods. Repository deletion is separately
  gated.
- Repository transfer is not exposed by the installation-token mutation
  provider. GitHub requires a user access token and completes transfer
  asynchronously after a `202 Accepted` response that still identifies the
  original owner. Transfer remains a validated lifecycle intent but is
  fail-closed until a dedicated actor-bound adapter is designed and tested.
- Every provider method sends one request, applies mutation spacing, returns
  typed redacted evidence, and never retries.
- The read provider owns before/after observation. Mutation handlers use the
  existing plan/apply/verify journal and stale-state preconditions.
- Governance plans bind a stable digest of identity, lifecycle, merge,
  security, Actions, workflow-token, and ruleset state. Volatile timestamps,
  request IDs, rate data, and permission evidence are excluded from that
  concurrency digest.
- Merge settings, Actions policy, selected Actions, and workflow-token policy
  are separate journal steps. Every step re-observes the complete governance
  state immediately before and after its one provider write.
- GitHub exposes selected-actions settings only while `allowed_actions` is
  `selected`. A transition from another mode is therefore an explicit
  discovery barrier: change the Actions policy, record the newly observable
  post-state, then re-plan selected-actions instead of guessing it.

## Consequences

- The read controller cannot mint a Mutation App token.
- A leaked or misrouted repository name cannot expand an already bound
  mutation scope.
- Live enablement requires separate App creation, selected-repository
  installation, exact permission evidence, and explicit external approval.
- Provider primitives and governance handlers can be fixture-tested without
  enabling a live write.
- A transfer cannot be mistaken for an immediately verified repository update
  or authorized by a machine installation token.

## Verification

- Schemas and estate cross-file validation cover exact capability references.
- Runtime tests reject shared App identity and shared secret locators.
- Provider tests cover repository binding, operation subsets, one-second
  spacing, no force, no bypass payload, and one-request transient failure.
- Lifecycle tests prove that transfer is blocked both by the canonical
  readiness gate and by the handler before any provider write.
- Governance handler fixtures pass through the full durable
  plan/apply/verify engine, including sequential exact-digest steps and stale
  unrelated-state rejection. At ADR acceptance the canonical estate blocked
  apply because its selectors were `observe-only` and `mutation_mode` was
  `disabled`; the later controlled rollout permits only managed NDDev sources
  and retains all capability, approval, enablement, and freshness gates.

## Rollback

Remove the mutation capability/runtime documents and provider factory. The
Inventory App and read controller remain unchanged; no GitHub state rollback
is needed because this stage performs no live write.
