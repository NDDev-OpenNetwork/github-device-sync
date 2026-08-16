# NDDev module harness bridge v2

Status: canonical GDS identity validation and explicit two-root parity are
implemented.

## Ownership

`harnesses/module-bridge.yaml` is the GDS-owned canonical identity bridge
between the independently managed NDDev public-module inventory and the GDS
harness catalogue. Its contract identifier is
`nddev-module-harness-bridge/v2`.

Each mapping stores only the nonderivable relationship:

- NDDev module ID;
- GDS harness ID;
- lifecycle;
- real historical module aliases, when any exist.

GDS owns harness identities and aliases in
`harnesses/capability-registry.yaml`. NDDev owns its module inventory, public
repository locators, gitlinks, and private validation slices in
`config/repositories.json`. The bridge does not copy derived module paths,
validation paths, public repository locators, stable repository IDs, contract
versions, capability versions, harness aliases, mutable public contracts,
profiles, or private evidence digests.

Explicit parity derives repository locators and contract expectations from the
NDDev registry, stable repository IDs from the NDDev repository relationship
anchor, and capability versions from GDS profiles. It separately computes the
digest of the NDDev registry contract expectation and the digest of the actual
public contract read from the exact stage-zero gitlink commit. Capability
profile digests are also computed and reported as evidence. None of these
derived values or digests is persisted into bridge identity.

The top-level relationship scope and evidence repository are stable ownership
invariants. `runtime_tests.required` remains false because behavioral setup
proof belongs to the private NDDev validation slices, not to GDS.

## Single-repository validation

```bash
gds harness bridge validate --gds-root <absolute-gds-root>
```

This command reads only the exact GDS checkout. It validates the bridge schema,
catalogue coverage, identity and alias collisions, lifecycle rules, and device
selections. Ordinary GDS validation therefore never depends on an NDDev
checkout.

The identity digest is computed from a canonical ordering of the stable bridge
identity. Mapping and alias order do not affect it. Input formatting, generated
paths, device selections, and private runtime evidence are not bridge identity.

## Explicit two-root parity

```bash
gds harness bridge parity \
  --gds-root <absolute-gds-root> \
  --nddev-root <absolute-example-harnesses-root>
```

Both roots are required, absolute, non-symlink, exact Git top-level paths. The
command is read-only and does not infer workspace layout, fetch refs, update an
index, or inspect provider state.

Parity derives `modules/<module-id>` and `validation/<module-id>`, then compares
the bridge with the NDDev module registry, repository anchor, `.gitmodules`,
and stage-zero gitlinks. Public contracts are read with trusted, isolated Git
from the exact gitlink object rather than from a potentially different
submodule worktree checkout. The command computes exact input digests for both
registries, all GDS harness profiles, GDS device selections, the NDDev anchor
and Git topology, plus one aggregate parity digest. These digests are evidence
returned by the command; none is persisted into the bridge.

Missing or mismatched NDDev evidence fails only this explicit parity command.
It cannot make GDS-only validation depend on another checkout.

Parity also reads each public module's canonical `.gds/repository.yaml` from
the exact gitlink. That repository-owned typed record must keep agent
instructions repository-owned and require exactly
`python3 cli-tools/validate_public_contracts.py` as its sole test command. The
validator path at that gitlink must be a tracked regular `100644` or `100755`
blob, never a symlink or another Git object type. These downstream projection
facts are derived parity evidence; they are not copied into bridge identity.

## Lifecycle

- `active` identifies a current module/harness relationship and cannot claim
  historical aliases.
- `renamed` identifies the current module ID and requires at least one
  bridge-owned historical module alias.
- `retired` reserves a historical identity. It must be absent from the current
  NDDev registry and relationship anchor and cannot remain selected by a device.

Canonical module IDs, harness IDs, derived GDS repository IDs, derived
case-folded public repository locators, and aliases must each have one owner.
Harness aliases are derived from the GDS registry and never repeated in the
bridge. Module aliases use the public module ID shape and are reviewed
relationship history, not aliases inferred from current repository names.
