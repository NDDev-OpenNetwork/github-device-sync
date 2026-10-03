# GDS estate desired-configuration v1 contract

Status: implemented. The shipped example estate enables controlled mutation
only for selectors and operation capabilities that explicitly allow it.

## Source boundary

`estate/` is the private desired-configuration authority for provider discovery
roots and sparse classification intent. It contains no provider inventory,
tokens, private keys, local paths, branch state, webhook payloads, or operation
journals.

The canonical tree contains:

```text
estate/estate.yaml
estate/installations/*.yaml
estate/mutations/*.yaml
estate/owners/*.yaml
estate/selectors/*.yaml
```

The shipped example declares five owners: `example-user`, `example-org`,
`example-media`, `example-guild` (observe-only), and the publisher account
`NDDev-OpenNetwork`. Installation IDs remain logical; their actual GitHub App and
provider installation IDs are bound only by a private device-local
`github-runtime` document, or by the gh-CLI credential variant (ADR 0034).
Estate secret references are portable `secret:gds/...` identities; they do not
select a keychain or CI backend.

`estate/installations/` declares read-only Inventory App roots.
`estate/mutations/` separately declares selected-repository Mutation App
capabilities. Mutation capabilities never replace repository identity or
discovery installations; they bind an exact operation set, exact write
permissions, lifecycle scope, and non-bypassable gates.

## Controlled mutation posture

```text
discovery.default_management_mode = observe-only
rollout.mutation_mode = pull-request
```

The `organization-sources` and `opennetwork-sources` selectors assign
`managed`; the other shipped selectors assign `observe-only`. Managed does not authorize an automatic write: apply
also requires an exact immutable plan, signed approval, one-shot enablement,
fresh compare-and-swap evidence, an operation-scoped mutation capability, a
private runtime, and the device mutation kill switch. Archive, fork, server,
guild, personal, and example-media selectors remain outside this rollout.

## Discovery and classification

Repositories are enumerated through GitHub App installation roots. Selectors
classify source repositories and forks by registered owner without a manually
maintained row per repository.

The compiler:

- accepts at most 2000 observed repositories;
- rejects duplicate provider IDs;
- matches owner login case-insensitively;
- applies the unique highest-priority selector;
- rejects equal-priority selector ambiguity;
- emits deterministic provider-ID order;
- reports GDS identity `not-observed` when only provider metadata was read.
  Listing a repository cannot prove whether its GDS anchor exists. Inventory
  reconciliation therefore does not manufacture identity drift or an onboarding
  request for every listed repository; use an actual anchor/workspace audit to
  establish that fact. Historical `unassigned` assignments remain readable and
  retain their original JSON representation for signed audit verification.

Repositories are classified by their owning account. The tenancy model is
exactly the provider account: one personal user account and otherwise
organizations by their exact provider login; an estate with no need for a
purpose grouping assigns each repository the owner's tenancy portfolio and
nothing else. The legacy `match.fork` and `classification.fork_portfolio`
fields remain readable for compatibility but do not select a separate
portfolio. Archive and name-specific selectors retain their own priority.
Device placement no longer requires a purpose portfolio at all:
`materialization.include[].match` selects repositories by provider facts
(owner login, exact names, name prefixes, visibility contract, lifecycle) with
first-match-wins ordering, so a `server-` family can span several owners under
one root while every other repository lands under its owner's root.

The shipped selectors use these priority bands:

- `100` — generic per-owner classification (the owner's tenancy portfolio);
- `200` — specialized overrides matched on repository facts such as name
  prefixes (the `server-` family in the example estate).

There is no archived selector in the shipped example. The compiler preserves
the provider's `archived` observation, but classification follows the matching
selector. An estate that defines archival by ownership can assign its archive
owner to that owner's tenancy portfolio regardless of the provider flag. An
estate that instead wants provider-archived precedence must declare an
appropriate higher-priority selector. Equal-priority overlapping matches are
rejected by the compiler.

## Monotonic policy fields

The base policy declares monotonic security fields that can only strengthen
(or stay the same) as higher tiers apply their overrides. An attempt to weaken
one without a scoped, time-bounded exception emits `GDS_POLICY_MONOTONIC_WEAKENING`.
The eight monotonic paths and their strength orderings (weakest → strongest)
are:

| Path | Values (weak → strong) |
|---|---|
| `security.external_write_requires_approval` | `false` → `true` |
| `security.public_projection_scan` | `optional` → `required` |
| `context.private_parent_persistence` | `ephemeral-only` → `forbidden` |
| `agent.generated_projection_edit` | `warn` → `forbidden` |
| `security.secrets_in_repository` | `forbidden` (only value; always strongest) |
| `package_management.npm_family_on_managed_path` | `forbidden` (only value) |
| `package_management.mutable_version_resolution` | `forbidden` (only value) |
| `package_management.remote_stream_to_shell` | `forbidden` (only value) |

Fields whose schema is `const: "forbidden"` have no weaker alternative; the
monotonic guard ensures any unknown value is rejected as a weakening rather
than silently accepted. The compiler enforces these in `monotonicStrength`
(`core/compiler/compiler.go`).

## Cross-file validation

`gds validate estate` proves:

- every schema and closed-object contract;
- unique installation, owner, and selector identities;
- exact estate-to-installation references;
- exact estate-to-mutation-capability references and a valid underlying read
  installation for every mutation capability;
- owner-to-installation existence and account-login agreement;
- selector-to-owner existence;
- selector-to-source-portfolio agreement with the owner descriptor;
- unique device materialization rules (by selector name or by canonical match
  content), existing workspace-root references, and no workspace-root reuse
  across label selectors (trait matches may share a root);
- non-symlink source documents;
- that a device inventory recording a consumer checkout also records every
  submodule declared beneath it, and records each with `materialization:
  git-submodule` (`GDS_DEVICE_INVENTORY_SUBMODULE_MISSING`,
  `GDS_DEVICE_INVENTORY_SUBMODULE_MATERIALIZATION_INVALID`);
- that every owner and portfolio a policy selector names is declared by the
  estate (`GDS_ESTATE_POLICY_OWNER_MISSING`,
  `GDS_ESTATE_POLICY_PORTFOLIO_MISSING`).

A selector that names nothing is not inert. An unsatisfiable match is skipped
rather than rejected, so the policy compiles, reports no finding, and governs
no repository -- the estate reads as governed while the profile never applies.
Resolving the reference here is what makes that difference visible before a
compile rather than after an incident.

## Observed, empty, and unobserved devices

A device's `repositories` block is observed evidence, not intent, and it is
optional on purpose. The three states are distinct and must not be collapsed:

| State | Meaning |
|---|---|
| block absent | the device has never been inventoried |
| `repositories: []` | the device was inventoried and holds nothing |
| entries present | the device was inventoried and holds those checkouts |

Absence is therefore a legitimate state rather than a defect, which is why no
validator reports it as a finding: a device that has not been observed must not
be described as empty, and refusing the estate over it would contradict the
schema that makes the block optional.

It is not silent either. `gds validate estate` reports
`devices_without_inventory` beside `devices`, so a reader can tell "three
devices" from "three devices, one of which nobody has looked at". The
submodule-consistency rule above applies only to devices that carry an
inventory; there is nothing for an absent one to contradict.

A device descriptor may declare an optional `class:` block
(`profile`/`gui`/`docker_mode`/`execution_policy`, plus server-baseline `hardening`)
whose vocabulary mirrors the `modules/macos-ubuntu-bootstrap` targets block, so
the descriptor's intent and the OS installer it drives cannot disagree. The
`GDS_DEVICE_CLASS_*` rules (for example `GDS_DEVICE_CLASS_MACOS_CONFLICT`,
`GDS_DEVICE_CLASS_SERVER_GUI`, `GDS_DEVICE_CLASS_EXECUTION_POLICY`) enforce
profile/OS consistency and never mutate configuration. The phased bootstrap seam
that consumes this block is documented in `docs/runbooks/bootstrap-device.md`.

`gds context` binds the device-local locator `device_id` to exactly one
`estate/devices/*.yaml` as `context.device` (including `class`). A missing
locator omits the field; context never infers a device from hostname or cwd.

No validator fixes configuration automatically.

Changing a device workspace-root mapping updates desired placement only. GDS
does not silently move or overwrite an existing checkout. Existing flat server
checkouts require a separately reviewed workspace migration that observes the
repository identity, proves a conflict-free destination, and applies an
explicit plan with rollback evidence.
