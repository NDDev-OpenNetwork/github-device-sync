# ADR 0021: Exact provider permissions and repository governance evidence

Status: Accepted

Date: 2026-07-11

## Context

A valid GitHub App installation token does not prove least privilege. The token
response carries effective permissions and repository selection, while estate
intent must define which capabilities are allowed. Repository identity alone
also does not prove merge, security, Actions, workflow-token, or ruleset state.

## Decision

- Every installation descriptor declares one exact repository selection and
  exact repository/organization permission maps.
- The read-only Inventory App contract is `all` repositories plus repository
  `actions`, `administration`, `checks`, `contents`, `metadata`, and
  `pull_requests` at `read`; organization permissions are empty.
- A token with a missing, stronger, additional, malformed, or differently
  scoped permission is rejected before any provider data request.
- Token values never enter evidence. Sanitized evidence records only expected
  permissions, effective permissions, repository selection, and exact-match
  status.
- Targeted repository observation reads metadata, merge/security settings,
  repository Actions policy, the complete selected-actions contract when
  applicable, workflow-token defaults, and at most 100 effective repository
  rulesets. Cross-origin pagination and larger ruleset sets fail closed.
- Governance-related webhook events persist this typed governance snapshot;
  high-volume code/check events perform one authoritative metadata read. Full
  estate inventory remains bounded and does not multiply every scheduled run
  into thousands of governance API requests.
- Governance reads default to observed evidence. ADR 0022 adds an optional
  exact local-policy comparison without introducing write capability.

## Consequences

- An accidentally over-privileged App cannot silently operate as the Inventory
  App.
- Changing App permissions or repository selection requires a reviewed estate
  contract change and renewed runtime evidence.
- Repository governance can be inspected or compared with a validated local
  policy without introducing a write-capable provider.
- Live App permissions, account plan behavior, and repository governance remain
  `NOT_PROVEN` until the target installations are inspected.

## Verification

- Schema fixtures cover the permission contract.
- Provider tests reject missing, additional, and stronger permissions before an
  HTTP request.
- Governance fixtures cover exact endpoints, normalized settings, ruleset
  ordering, bounded pagination, request IDs, and permission evidence.
- Controller tests prove authoritative governance persistence and preserve
  inaccessible repositories as inaccessible rather than deleted.

## Rollback

Remove the targeted governance reader and revert the installation permission
contract together. No GitHub state changes are required because this stage is
read-only.
