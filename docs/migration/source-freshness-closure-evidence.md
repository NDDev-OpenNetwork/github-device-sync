# Source freshness closure evidence

Status: semantic baselines accepted locally; runtime-dependent claims remain
`NOT_PROVEN`

Evidence date: 2026-08-13

Normative source register: `docs/source-register/sources.yaml`

## Closed contract

Source verification now rejects a baseline unless two consecutive bounded
fetches return the same content digest. The plan records that stable digest;
apply rechecks it, materializes the exact reviewed register candidate, and
verify proves the journaled result. A changing representation returns
`GDS_SOURCE_CONTENT_NONDETERMINISTIC` and cannot become tracked authority.

The checker asks official servers for machine-readable representations before
HTML. Claude documentation uses its official Markdown endpoints. GitHub
release and tag watches use official Atom feeds rather than hydration HTML or
unauthenticated REST calls, avoiding request-specific markup and API quota as
verification inputs.

## Evidence

- Registered sources: 59.
- Sources with an approved content digest: 59.
- Missing content digests: 0.
- Sources refreshed through exact signed `plan -> approve -> enable -> apply ->
  verify` operations on 2026-08-13: 52.
- Post-refresh freshness classification: 59 current, 0 overdue, 0 blocked,
  and 0 missing evidence baselines.
- Consecutive-fetch stability failures after representation normalization: 0.
- Verification mutations used `plan -> apply -> verify`; no register entry was
  updated by a direct edit.
- Current stable release facts were rechecked for Cobra, jsonschema, go-yaml,
  `actions/attest`, `actions/checkout`, `actions/setup-go`, and
  `actions/upload-artifact`.
- The release workflow now pins `actions/checkout@v7.0.0` and
  `actions/upload-artifact@v7.0.1` by full commit SHA. Both run on Node.js 24;
  the workflow uses `ubuntu-latest`, and its unchanged inputs remain supported.

The tracked register intentionally contains semantic baselines, review dates,
statuses, and governed claims. Request timestamps, byte counts, HTTP metadata,
and operation IDs remain in the local append-only operation journal rather
than creating tracked churn.

## Commands

```text
GOTOOLCHAIN=go1.26.5 go test ./core/source ./core/app
python3 scripts/validate_gds_schemas.py --root . \
  --fixtures tests/fixtures/schemas/v1/cases.json --json
gds source mark-verified --plan ...
gds source mark-verified --apply <plan-id> ...
gds source mark-verified --verify <operation-id> ...
gds source check --id <each-of-57> --json
actionlint .github/workflows/release-bundle.yml
```

## Remaining proof boundary

Source records governing volatile runtime behavior retain a status that
contains `runtime-not-proven` or an equivalent external proof qualifier.
Therefore aggregate source freshness remains `NOT_PROVEN` for release
promotion even though every semantic source representation is pinned and
unchanged. Promotion requires the exact harness, GitHub App, hosted
attestation, or workflow runtime evidence named by each governed claim; this
document does not manufacture that evidence.
