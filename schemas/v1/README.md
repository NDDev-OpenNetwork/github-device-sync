# GDS v1 schema contracts

Status: migration baseline. These contracts do not switch the legacy runtime.

## Authority

- JSON Schema files in this directory define the portable data shape.
- `.gds/repository.yaml` owns repository-specific GDS facts.
- `policy.schema.json` owns reusable policy-source shape;
  `compiled-policy.schema.json` owns deterministic effective output and leaf
  provenance.
- `bundle-lock.schema.json` owns exact bundle and generated-file digests.
- `github-runtime.schema.json` owns private device-local GitHub App identities
  and logical-reference-to-secret-backend bindings; it never contains secret
  values.
- `mutation-capability.schema.json` owns the canonical write scope, exact
  permissions, supported operations, and hard gates for a separate Mutation
  App. `github-mutation-runtime.schema.json` binds those capabilities to
  distinct private device-local App identities and secret locators.
- `controller-runtime.schema.json` owns private loopback service, state,
  scheduling, and backup bindings; it references but does not duplicate estate
  or GitHub runtime intent.
- `estate-registration.schema.json` owns the minimal device-local locator that
  binds one physical control-plane checkout to its stable repository identity
  and exact repository-anchor digest.
- `audit-snapshot.schema.json` owns signed reconciliation evidence shape; code
  verification additionally requires the controller-pinned Ed25519 public key.
- `schemas/migrations/registry.yaml` owns schema migration registration.
- GitHub and local Git remain authoritative for observed provider and worktree
  state; observed state is not copied into repository anchors.

## Serialization

- Schemas use JSON Schema Draft 2020-12.
- Canonical YAML is YAML 1.2-compatible UTF-8 with LF line endings.
- Duplicate keys, aliases, anchors, merge keys, and ambiguous YAML 1.1 boolean
  spellings are rejected before schema validation.
- Closed objects reject unknown fields. Explicit dynamic maps are the only
  exception.
- Portable paths start with `~/` or an uppercase environment variable such as
  `${XDG_STATE_HOME}`; absolute device paths are observed state.
- Portable estate secret references use `secret:gds/...`; backend-specific
  locators exist only in private device runtime configuration.

## Identity

GDS IDs use a typed prefix and one canonical 26-character ULID. The first ULID
character is restricted to `0` through `7`, which excludes overflow values.
Repository IDs never change when a GitHub repository is renamed, transferred,
or materialized at another filesystem path. Provider IDs and owner/name
locators are separate fields.

## Validation

Run:

```bash
python3 scripts/validate_gds_schemas.py --json
python3 -m unittest tests/schema/test_validate_gds_schemas.py
```

The validator is read-only and emits the v1 operation result envelope. Exit
classes used in this migration phase are `0` for success, `2` for validation
failure, `4` for invalid input, and `14` for an internal error.

The Python validator is a bootstrap gate for schema-first migration. Once the
production `gds validate schemas` command reaches fixture and output parity,
the bootstrap entry point will become a thin compatibility wrapper or be
retired explicitly; both implementations must not remain independent policy
authorities.
