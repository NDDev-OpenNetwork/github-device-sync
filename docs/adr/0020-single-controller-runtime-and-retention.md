# ADR 0020: Single-controller runtime, loopback ingress, and retention

Status: Accepted

Date: 2026-07-11

## Context

The first production-capable GDS controller must support durable webhooks,
scheduled reconciliation, evidence, and recovery without introducing an
untested distributed system. It also must not expose an unauthenticated Go HTTP
service directly to the public network or retain webhook payloads forever.

## Decision

- The initial runtime is one active controller process over SQLite in WAL mode
  with one durable queue/state database.
- The process binds only to an explicit loopback IP. Public webhook traffic must
  terminate TLS and enforce network policy at a separately managed reverse
  proxy before forwarding to the loopback webhook path.
- The service exposes loopback health, readiness, and non-secret metrics;
  structured logs never contain payloads, tokens, secret values, or arbitrary
  provider error text.
- Startup requires one private schema-validated controller runtime document and
  one private GitHub runtime document. Provider reads use an Inventory App
  identity only; no mutation provider is constructed.
- Every accepted webhook is durably queued before `202`. Repository events
  trigger an authoritative targeted provider read; installation-wide events
  trigger bounded full reconciliation.
- A consistent verified SQLite backup is created before retention maintenance.
  Default policy is 14 retained backups, 14 days for terminal webhook payload
  records, 400 days for reconciliation journals, and indefinite retention for
  mutation operation/security journals.
- Full reconciliation creates an Ed25519-signed private audit snapshot using a
  dedicated secret reference and a separately pinned expected public key.
- Move to PostgreSQL plus a durable external queue before enabling more than one
  active controller instance, when an HA requirement is approved, or when
  measured SQLite/queue latency violates the accepted service budget.

## Consequences

- The first deployment has one failure domain and a simple restore procedure.
- Horizontal scaling is intentionally blocked until storage leases, ordering,
  and failover are proven against a shared backend.
- A reverse proxy and GitHub webhook endpoint remain separate deployment
  approvals; loopback service implementation does not authorize either.
- GitHub plan capabilities, App existence, effective permissions, and live rate
  behavior remain `NOT_PROVEN` until inspected in the target accounts.

## Alternatives considered

- PostgreSQL and a distributed queue immediately: rejected until HA is a real
  requirement and single-controller measurements show a need.
- Bind the Go process to `0.0.0.0`: rejected because TLS, exposure, and network
  policy belong at an explicit deployment boundary.
- Polling without webhooks: rejected because it wastes provider budget and
  increases drift latency.
- Webhooks without periodic full reconciliation: rejected because deliveries
  can be missed, duplicated, delayed, or reordered.

## Verification

- Runtime schemas reject public listeners, relative paths, unknown fields, and
  unsafe private-file permissions.
- Service tests cover health/readiness/metrics, graceful shutdown, scheduled
  jobs, HMAC ingress, queue retry/dead-letter, targeted observation, full
  reconciliation, pinned-key audit verification, verified backup, and
  retention boundaries.
- Scale and live deployment gates remain separate acceptance evidence.

## Rollback

Stop the controller, revoke or disable its Inventory App installation token
source, disable webhook forwarding, and restore the last verified SQLite
backup. Repository and GitHub mutations are impossible because no mutation
provider is linked into this runtime.
