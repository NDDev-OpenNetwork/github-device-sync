# GDS context resolution v1

Status: implemented locally

## Resolution order

`gds context` resolves the current Git worktree and repository anchor first,
then selects one trusted estate root in this order:

1. explicit process-local `GDS_ESTATE_ROOT`;
2. the current repository when it has the `control-plane` role;
3. the device-local XDG estate registration.

No filesystem ancestor or repository name is treated as estate authority.

## Capability reporting

`context.capabilities` reports typed implementation support separately from
runtime activation and policy. Provider observation and external mutation
adapters are implemented, but both require explicit device-local runtime
configuration. Observation is read-only. Mutations remain subject to explicit
approval plus each command's plan/apply/verify and effective-policy checks.

The definitions and their registered top-level command carriers come from the
canonical `core/capabilities` registry. CLI registration, context output, and
the operations contract are checked against that registry so adding or removing
a carrier cannot silently leave another surface stale.

Capability reporting is routing metadata, not authorization. A command must
still load and validate its exact runtime, repository scope, approval, and
effective policy before performing any external action.

## Device registration

The default path is:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/github-device-sync/estate-registration.json
```

The file validates against `schemas/v1/estate-registration.schema.json` and
contains only:

- `device_id`;
- stable control-plane `repository_id`;
- canonical physical `root`;
- exact repository-anchor digest.

Resolution rejects missing, symlinked, oversized, malformed, identity-drifted,
role-drifted, moved, or anchor-drifted registrations. Missing registration is
`NOT_PROVEN`, never an implicit fallback to directory ancestry.

## Applied policy provenance

`policy.provenance` is `verified` only after two independent layers agree:

1. the applied bundle lock schema, ordered output digest, compiled-policy
   identity, and every locked file digest are internally consistent;
2. the repository anchor, lock, and managed projections match committed Git
   state, and the development candidate is independently reconstructed from
   the committed canonical estate source boundary plus the templates embedded
   in the running GDS binary.

The canonical reconstruction must reproduce the exact bundle source commit,
bundle digest, compiled policy, input/output digests, managed files, and lock.
A replacement lock with correspondingly replaced files therefore cannot act as
its own trust root, even when all of its internal digests were recomputed and
the replacement was committed in the target repository.

Hosted pull-request checks may execute a synthetic merge commit rather than the
reviewed head. Canonical source resolution treats such a merge as transparent
only when one parent's complete declared source boundary is identical. A merge
with a conflict resolution or any other source-boundary change retains its own
commit identity and therefore requires a correspondingly regenerated lock.
This proof requires the relevant parent commit objects: an ancestry-truncated
checkout is insufficient and remains fail closed rather than trusting event
metadata or the applied lock.

Non-development bundles fail closed as `NOT_PROVEN` until independently
verified release evidence is available. A committed target-repository lock is
not a substitute for provenance/SBOM attestations and the separately pinned
consumer trust root.

## Mutation contract

```text
gds workspace register-estate --plan
gds workspace register-estate --apply <plan-id>
gds workspace register-estate --verify <operation-id>
```

Plan is side-effect-free except for storing the immutable plan in the existing
private GDS state database. Apply requires exact device/session identity and a
bounded approval reference. It rechecks control-plane HEAD, stable identity,
anchor digest, effective policy digest, and existing registration state before
one confined atomic write. Verify reads journaled after-evidence and the live
registration.

The registration is device configuration, not a tracked projection, observed
estate snapshot, secret, or reusable policy source.
