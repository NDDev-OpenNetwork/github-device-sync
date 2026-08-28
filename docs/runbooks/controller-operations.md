# GDS single-controller operations

Status: local implementation runbook; live deployment is not authorized by
this document.

## Preconditions

- Build the exact reviewed `gds-controller` source with the pinned Go toolchain.
- Provision a private `github-runtime` file and private `controller-runtime`
  file that pass their v1 schemas.
- Provision the Inventory App private key and webhook HMAC secret in the
  selected device secret backend. Provision a separate Ed25519 audit signing
  key and pin its corresponding base64url public key in controller runtime.
  Never put secret values in YAML, shell
  history, repository files, logs, or fixtures.
- Verify App installation IDs, `all` repository selection, account ownership,
  and the exact effective read-permission map from the token response. Missing,
  extra, or stronger permissions are blocking findings. Record unavailable
  checks as `NOT_PROVEN`.
- Create and approve the reverse proxy/TLS plan separately. The Go service must
  remain loopback-only.

## Build and local validation

```bash
GOTOOLCHAIN=go1.26.7 go build -trimpath -o /tmp/gds-controller ./core/cmd/gds-controller
/tmp/gds-controller --version
python3 scripts/validate_gds_schemas.py --root . --json
GOTOOLCHAIN=go1.26.7 go test ./core/controller ./core/webhooks ./core/state -race -count=1
```

## Start

```bash
/absolute/path/gds-controller \
  --runtime-config /absolute/private/path/controller-runtime.yaml
```

Expected startup evidence:

- one `controller-started` JSON event;
- `/healthz` returns `200`;
- `/readyz` returns `200` after service initialization;
- the first reconciliation and backup produce terminal metrics/events;
- the first reconciliation produces a signed audit snapshot whose pinned key,
  payload digest, and signature verify;
- no provider mutation is attempted.

Do not forward public traffic until local health, HMAC rejection/acceptance,
queue processing, full reconciliation, backup verification, and restore have
been observed on the deployment host.

## Monitor

Inspect only the loopback endpoints:

```bash
curl --fail --silent http://127.0.0.1:8787/healthz
curl --fail --silent http://127.0.0.1:8787/readyz
curl --fail --silent http://127.0.0.1:8787/metrics
gds state inspect --path /absolute/private/path/state.db --json
gds github governance \
  --runtime-config /absolute/private/path/github-runtime.yaml \
  --installation installation:github-personal \
  --owner example-user \
  --repository exact-name \
  --compare-local \
  --json
```

Investigate any increase in:

- `gds_webhook_rejected_total`;
- `gds_webhook_queue_failed`;
- `gds_webhook_queue_dead_letter`;
- `gds_reconciliation_partial_total`;
- `gds_reconciliation_failed_total`;
- `gds_backup_failed_total`.

Provider rate/request evidence is stored in reconciliation results. Do not log
or export webhook payloads as monitoring labels.

The governance inspection command is read-only. Without `--compare-local`,
`comparison.status: observed-only` makes no compliance claim. With the flag,
the command returns field-level drift only after exact local/provider identity
and compiled-policy validation. It still does not apply remediation.

Governance remediation is a separate explicit operation. Always inspect and
review the stored plan before supplying an approval reference. Never enable
estate mutation mode or convert a selector to `managed` merely to make a plan
apply. When a plan reports `requires_replan: true`, verify its first operation
and create a new plan; this is the intentional selected-actions discovery
barrier, not a retry failure.

## Stop and kill switch

Send `SIGTERM` and wait for graceful shutdown. The wait is bounded:
`shutdown_timeout_seconds` is one total budget covering both HTTP draining and
the background worker drain, so the process cannot hang on a worker,
reconciliation, or backup run that ignores cancellation. A clean stop exits
zero. An exhausted budget reports a shutdown-timeout error and exits non-zero;
read that as "the previous owner did not prove a clean release of its SQLite
lifetime authority" and confirm the process is gone before starting a
successor against the same state database.

Emergency response order:

1. stop reverse-proxy forwarding;
2. stop `gds-controller`;
3. revoke/disable the Inventory App secret or installation when compromise is
   suspected;
4. preserve state, backups, and logs for investigation;
5. do not mark inaccessible repositories deleted.

No controller mutation credential exists in this phase.

## Restore

1. Stop the controller and reverse-proxy forwarding.
2. Preserve the private audit directory and its pinned public-key configuration.
3. Copy the selected verified `state-YYYYMMDDTHHMMSSZ.db` to a new private
   `0600` state path; never overwrite the only existing state file.
4. Run `gds state inspect --path <restored-path> --json`.
5. Point a private controller runtime candidate to the restored path.
6. Start locally without public forwarding and verify health, state schema,
   queue, and a full read-only reconciliation.
7. Re-enable forwarding only after drift, audit, and dead-letter review.

Restore does not roll back GitHub because this controller has no provider write
capability.
