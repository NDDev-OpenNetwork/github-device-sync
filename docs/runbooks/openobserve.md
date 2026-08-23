# GDS OpenObserve operations

The local SQLite operation journal is the recovery authority. OpenObserve is an
asynchronous search, dashboard, and alert sink; its outage never changes an
operation result.

## Supported boundary

The reviewed asset contract targets OpenObserve `v0.90.3` or newer and lives at
`observability/openobserve/gds-operational-signals.json`. OpenObserve supports
JSON dashboard and alert import, but its exported dashboard object is
version-specific. Therefore the tracked file intentionally owns portable SQL,
panel identity, alert semantics, and minimum version rather than pretending a
UI-exported object from an unobserved instance is portable.

Before deployment, create three bounded streams for OTLP logs, metrics, and
traces, or an OpenObserve pipeline that projects all three into the stream
named by `GDS_OTEL_STREAM`. Replace only the exact `${GDS_OTEL_STREAM}` token in
the reviewed queries. Do not interpolate credentials, organization names, or
destinations into the tracked asset.

Configure each GDS process with an HTTPS OTLP base endpoint and a runtime-only
authorization value:

```text
GDS_OTLP_ENDPOINT=https://openobserve.example/api/<organization>
GDS_OTLP_AUTHORIZATION=<runtime secret>
```

The credential must come from the device secret store. It must never appear in
Git, operation evidence, dashboard JSON, logs, metrics, or traces.

`core/telemetry.validateTransport` enforces this contract in code rather than by
convention, both when the exporter is built from the environment and on every
flush, so a directly constructed configuration cannot bypass it:

- HTTPS is required whenever any credential-bearing header is configured.
- Remote plaintext HTTP is refused with or without a credential.
- Plaintext HTTP is accepted only for a loopback endpoint carrying no credential,
  which is the local-collector case (`http://127.0.0.1:4318`).
- Redirects are never followed. A redirect can move the request to another
  origin, so the export fails closed and the event stays in the durable outbox
  for the next flush instead of replaying the credential elsewhere.

A transport rejection never blocks or reverses the local operation: the journal
remains the recovery authority and the outbox retains the event.

## Deployment and verification

1. Confirm the target reports OpenObserve `>=0.90.3`.
2. Create a dedicated `gds` folder and dashboard in the UI.
3. Add the six panels from the tracked contract, preserving IDs, signal type,
   SQL, and titles.
4. Import or create the four scheduled alerts with the exact five/fifteen
   minute periods and `value > 0` conditions. Bind notification destinations
   separately; destinations are externally managed secret-bearing state.
5. Export the resulting dashboard and alerts from OpenObserve and store their
   digests in the deployment evidence, not in this repository.
6. Use the next normal GDS operation to verify ingestion and queries. Retain
   pending/retry/drop evidence when a real endpoint outage occurs and prove the
   local operation still finalized. Do not emit a synthetic event, manufacture
   an outage, rerun an operation or block normal work merely to close this
   evidence gap; until natural evidence exists, report it as `NOT_PROVEN`.

Re-export and re-verify after any OpenObserve upgrade. A target older than the
minimum version, a changed query, a missing panel/alert, or an exporter that can
block operation finalization is `NOT_PROVEN`, never an implicit success.
