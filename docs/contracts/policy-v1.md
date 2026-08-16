# GDS policy compiler v1 contract

Status: Phase 04 local contract. No managed repository rollout is authorized.

## Canonical source

Reusable policy sources live below `policies/` and validate against
`schemas/v1/policy.schema.json`. A repository selects policy IDs in
`.gds/repository.yaml` under `policy.profiles`.

Directory placement is organizational. These source fields are authoritative:

- `policy.id`;
- `policy.tier`;
- `policy.priority`;
- `policy.distribution`;
- optional selectors;
- `apply` values and explicit list mutations;
- monotonic constraints.

The loader rejects symlinked policy sources, duplicate IDs, invalid schemas,
non-portable serialization, paths outside the estate root, and more than
10,000 source files.

## Selection and precedence

A selector names estate identities, not provider ones. `match.owner` carries an
estate owner id and is resolved through `estate/owners/`, keyed by the
`provider_login` each owner declares; a repository anchor carries the GitHub
login, and those are two vocabularies. The compiler does not bridge them by
transforming one into the other. Deriving an id from a login was tried and was
wrong for four of the five declared owners, and because an unsatisfiable match
is skipped rather than rejected, every affected profile silently governed
nothing.

Two ways for that resolution to fail are reported rather than resolved:
`GDS_POLICY_OWNER_REGISTER_UNAVAILABLE` when the register cannot be read, and
`GDS_POLICY_OWNER_REGISTER_AMBIGUOUS` when one provider login is claimed by more
than one owner id. GitHub logins are unique, so a collision means the estate
declares one account twice, and choosing either id would decide silently which
policies apply.

Every selected profile must exist and its selector must match the repository.
Sources are applied in this fixed order:

```text
base -> owner -> portfolio -> role -> stack -> lifecycle -> repository
```

Within one tier, lower priority is applied first and higher priority may
override it. Two different policies claiming the same leaf at the same tier
and priority are a conflict; lexical file order never decides behavior.

Maps are merged only along schema-declared setting maps. Scalar leaves are
replaced by later permitted tiers. Agent profile lists use explicit
`append`/`remove`; an item in both operations in one source is invalid. Removal
is applied before append and final element order follows first surviving
appearance.

Repository GitHub governance is declared under `apply.github`. Each setting is
explicitly `managed`, `observed`, or `ignored`; only `managed` carries a
desired value. Selected Actions are one atomic contract containing
`github_owned_allowed`, `verified_allowed`, and the complete normalized
pattern allowlist. `github.releases.immutable` controls repository-level
immutable-release enablement; owner enforcement is observed provider state and
cannot be weakened by repository policy. The read-only governance comparator
never turns observed or ignored evidence into a remediation target.

Policy-source references are not part of schema v1, so cycles are not
representable. If references are added later, cycle detection becomes a schema
and compiler release gate.

## Security monotonicity

The current compiler knows strength ordering for:

- `security.external_write_requires_approval`;
- `security.public_projection_scan`;
- `context.private_parent_persistence`.

A source declaring a monotonic path must set it. Later policies cannot weaken
it. Unknown monotonic paths fail closed. Expiring exception objects are not yet
implemented; therefore no exception can currently bypass this gate.

## Distribution firewall

Every policy declares one distribution class:

```text
public < internal < private
```

- public repositories accept only public policy sources;
- internal repositories accept public and internal sources;
- private repositories may accept all three.

The compiler enforces this rule and the projection generator repeats it before
rendering standalone files.

## Compiled output

`schemas/v1/compiled-policy.schema.json` defines:

- repository ID and bundle version;
- canonical compiled digest;
- ordered source IDs, tiers, priorities, distribution classes, paths, and raw
  source digests;
- effective settings;
- provenance for every scalar or list-element leaf.

Semantic validation rejects missing/orphan provenance, provenance that does not
match its source record, duplicate or incorrectly ordered sources, and digest
mismatch. The digest preimage is canonical JSON containing schema version,
repository ID, bundle version, sources, effective settings, and provenance; it
does not include the digest field itself.

Arrays receive provenance per element, not at the array container. Replacing a
list also removes provenance for superseded elements, so shorter higher-tier
values cannot leave orphan provenance.

## Read-only command

```bash
gds compile policy --json
```

The command resolves the trusted estate root, validates the current repository
anchor and selected sources, and returns the compiled document. It writes no
file and reports no mutation.
