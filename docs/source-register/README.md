# GDS source register

`docs/source-register/sources.yaml` records volatile official facts used by the
engine, schemas and release verifier. Documentation and runtime observations
are evidence; they do not authorize a mutation or establish device acceptance.

The source schema, freshness classifier, content-change detector, exact review
transaction and release freshness gate are implemented:

```bash
gds source status --json
gds source check --id <registered-source-id> --json
gds source mark-verified --help
```

Run review transactions in the repository that owns this register. A public
module uses its own source/policy root even when a private estate is registered;
it does not acquire control-plane authority over that estate. A private control
plane can review its own register. Other repository roles fail with
`GDS_SOURCE_OWNER_ROLE_REQUIRED`. Plans still bind the exact source, committed
Git identity, semantic review evidence and approval; apply and verify retain
those same boundaries.

Review the actual governed claims before changing `verified_at`, `next_review`,
status or content digest. Changed bytes alone neither invalidate a claim nor
prove it remains true. Missing digests and unavailable official representations
remain explicit. The release builder and toolchain security floor are pinned
in this register and validated by `scripts/validate_go_core.sh`; a workstation's
installed version is not a public product fact.

Harness documentation establishes documented interfaces only. Runtime support
and stable-release active-seven evidence require their own exact, fresh tests
and signed producer records, as described in the release lifecycle runbook.
