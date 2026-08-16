# Phase 08 GitHub provider and controller evidence

Status: C7 implemented locally; live GitHub and deployment gates are
`NOT_PROVEN`.

Date: 2026-07-11

## Completed locally

- Added private, exact-set runtime binding for macOS Keychain, Linux Secret
  Service, environment, and private-file secret backends.
- Added GitHub App JWT signing, short-lived installation-token caching,
  redirect refusal, pinned API version, bounded responses, rate backpressure,
  and redacted provider errors.
- Made Inventory App permissions canonical per installation. Effective token
  permissions and repository selection must match exactly before any provider
  data request; tokens are never serialized.
- Added bounded installation inventory and `gds reconcile --plan` with account,
  installation, permission, access-state, request-ID, and rate evidence.
- Added `gds github governance` for one exact repository. It observes merge and
  available security settings, Actions policy, workflow-token defaults, and at
  most 100 effective repository rulesets. It reports `observed-only`; no C8
  desired governance is invented.
- Added a loopback-only single-controller service with HMAC ingress, durable
  queue, retry/dead-letter, scheduled reconciliation, verified SQLite backups,
  bounded retention, health/readiness/metrics, redacted JSON logs, and graceful
  shutdown.
- Added targeted event routing: high-volume code/check events use one metadata
  read; governance-related events use the full governance snapshot.
- Added private Ed25519-signed audit snapshots pinned to an expected public key.
  A full reconciliation cannot report success when audit creation fails.
- Added a 2000-repository integration fixture that persists observations,
  reopens the state database, isolates one installation outage, and resumes
  without duplicate observations or unbounded provider calls.

## Evidence

```text
GOTOOLCHAIN=go1.26.5 scripts/validate_go_core.sh --quick
GDS Go core validation: PASS (quick)

GOTOOLCHAIN=go1.26.5 go test -race \
  ./core/providers/github ./core/githubruntime ./core/reconciler \
  ./core/webhooks ./core/controller ./core/app ./core/estate
PASS

uv run --with-requirements requirements/test.txt --with pytest-cov \
  python -m pytest
27 passed

tools/test-sync.sh
64 checks, 0 failed

python3 scripts/validate_gds_schemas.py
PASS
```

The fixture suite proves exact permission rejection before HTTP, token-cache
isolation, same-origin pagination, governance normalization and bounds,
read-only webhook event contracts, HMAC/dedup/retry/dead-letter behavior,
inaccessible-state preservation, signed audit, backup/restore, and bounded
2000-repository persistence/restart/outage recovery.

## Not proven

- GitHub App creation, installation IDs, account plan, effective live
  permissions, and `all` repository selection;
- live token issuance/renewal, rate headers, secondary limiting, repository
  governance, PR/check state, or inaccessible private repositories;
- public HTTPS reverse proxy, webhook delivery latency/redelivery, controller
  hosting, HA, or operational retention on a deployment host;
- any GitHub write, mutation credential, ruleset/settings change, PR, release,
  or rollout;
- full trusted release evidence; the host default Go remains below the pinned
  release floor even though the exact toolchain is available through
  `GOTOOLCHAIN=go1.26.5`.

## External approval boundary

Creating/installing either GitHub App, provisioning live credentials, exposing
an endpoint, deploying the controller, or performing any provider write remains
an exact plan/apply/verify action with separate approval.
