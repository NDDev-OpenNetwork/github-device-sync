# GDS agent-first CLI v1 contract

Status: read-only inspection, compilation, validation, immutable release
verification, and rollout planning are implemented. Bounded local projection,
state, Git, repository, module, fork, workspace, and release-installation
mutations use plan/apply/verify. Live GitHub and package-provider mutations
remain policy-gated.

`gds-assurance` is a separate release-gate executable rather than an ordinary
estate command. It accepts an exact control-plane root, requires a clean
worktree, performs no network or external mutation, and emits a strict
`assurance-report` bound to the current source commit. Its fixed production
scenario covers 2000 repositories, two installations, 1000 forks, shared
modules, webhook load, SQLite restart, outage isolation, projection generation,
portfolio planning, rollout pause gates, kill switches, and measured resource
budgets.

## Result envelope

Every executed GDS workflow can emit `--json`. The output validates against
`schemas/v1/operation-result.schema.json` and contains:

- `schema_version`;
- exact command identity;
- result and matching exit class/code;
- optional scope and command data;
- typed findings with evidence;
- an explicit mutation record.

Read-only commands always return:

```json
"mutation": {
  "attempted": false,
  "completed": false
}
```

No command writes human text around a JSON envelope. CLI parse failures also
produce an envelope when `--json` was requested.

## Exit classes

- `0`, `success`: the requested read-only evidence is complete for its declared
  scope.
- `2`, `validation`: a deterministic contract or semantic invariant failed.
- `3`, `not-proven`: required local, bundle, provider, or freshness evidence is
  unavailable.
- `4`, `input`: CLI input, schema source, or serialization input is invalid.
- `5`, `stale`: a stored plan or exact precondition changed.
- `6`, `approval`: exact approval evidence is missing or does not
  cover the stored plan.
- `7`, `authorization`: required local or provider authorization failed.
- `8`, `conflict`: concurrency, forced-update, or lock evidence blocks apply.
- `9`, `policy`: effective policy forbids the operation.
- `10`, `partial`: independent subplans or saga steps have mixed outcomes.
- `11`, `provider-transient`: an external provider returned a transient failure.
- `12`, `security`: a security invariant blocked the operation.
- `13`, `unsupported`: the required capability/provider is intentionally not
  implemented or accepted.
- `14`, `internal`: the command crossed an unexpected internal failure
  boundary.

`NOT_PROVEN` is never converted to success. A missing bundle lock currently
causes context-dependent commands to exit `3` while still returning all verified
local context and Git data.

## Global flags

```text
--json
--cwd <directory>
--timeout <duration>
```

Timeout must be greater than zero. It covers context resolution and all Git
subprocesses used by the command.

## Commands

### `gds context`

Resolves:

- canonical current path;
- Git worktree and common Git directory;
- repository anchor and stable identity;
- standalone or embedded-submodule mode;
- trusted estate root when proven;
- bundle-lock presence;
- skill profile routing;
- independent mutation boundaries.

It does not invoke a skill, read remote instructions, or contact GitHub.
The estate root follows the exact override/self/device-registration order in
`docs/contracts/context-resolution-v1.md`.

### `gds status`

Uses Git porcelain v2 with NUL delimiters and reports independent axes:

- branch, detached, or unborn HEAD;
- cached upstream ahead/behind/diverged state;
- staged, unstaged, untracked, conflicted, and submodule changes;
- recursive submodule state;
- linked worktrees.

Ahead/behind values come from local remote-tracking refs. Remote freshness is
`unknown` until a later explicit refresh workflow records evidence; the command
never fetches or integrates.

### `gds discover`

Finds local Git boundaries below a filesystem root with bounded depth,
repository count, and worker concurrency. It does not clone missing
repositories. Multiple worktrees sharing one common Git directory are allowed;
the same stable repository ID in distinct Git stores is a conflict.

### `gds inventory`

Builds an ephemeral observed local inventory from discovery plus Git status. It
includes an observation timestamp and is not committed desired configuration.

### `gds validate`

Validates the current repository anchor. In the control-plane repository it
also validates embedded Draft 2020-12 schemas, canonical migration files, and
the positive/expected-negative fixture corpus. Validation reports only; it does
not fix files.

### `gds doctor`

Aggregates context, Git status, and applicable schema checks. Missing future
contracts such as `.gds/bundle.lock.yaml` remain explicit `NOT_PROVEN` findings.

### `gds compile policy`

Loads selected canonical policy profiles from the trusted estate root, applies
fixed precedence and monotonic security rules, and returns one effective policy
with leaf provenance. It does not write the compiled document.

### `gds generate repository`

Renders a candidate repository projection in memory and returns paths and
digests. File contents are not included in the result envelope. `--check`
compares the candidate against existing files without fixing drift.

### `gds validate projections`

Runs the same deterministic candidate generation and read-only drift check
under the validator command surface. Missing, manually edited, or symlinked
generated files are validation findings.

### `gds validate skills`

Validates the canonical `gds-*` registry, skill sources, profiles, Codex
sidecars, metadata budgets, and initial core eval corpus without invoking a
model or writing plugin copies.

### `gds skill package <plugin>`

Builds a deterministic standalone Codex plugin candidate in memory. The result
contains paths, sizes, registry digest, package digest, and file digests but no
file contents and no filesystem mutation.

### `gds validate plugins`

Builds and validates all three registered plugin candidates without installing
or trusting them.

### `gds validate harnesses --harness <id|all>`

Validates the exact canonical registry and versioned profiles. The default is
`all`. Runtime evidence remains `NOT_PROVEN` until isolated contract fixtures
pass for an exact harness version. Codex additionally validates its marketplace,
plugin packages, and hook contract.

### `gds harness detect --harness <id|all>`

Runs only each profile's bounded version command and returns observed binary
and version evidence. It never installs, updates, configures, authenticates, or
changes a support status. Missing or failing binaries are `NOT_PROVEN`.

### `gds harness sync --device <descriptor> --target-root <root>`

Read-only reconciliation of one device against the harness catalogue. It compares
the `harnesses:` selection in the device descriptor with the projections actually
installed at the target root and reports, per catalogue entry, which existing
single-harness transaction would converge it: `install`, `update`, `remove`, or
`current`. An entry that is neither selected nor installed is omitted.

It never mutates. The former `--converge` mode is removed and returns
`GDS_HARNESS_SYNC_CONVERGE_REMOVED`: one invocation created several plans and
could not bind one approval to every exact digest. Use the reported action to
run an explicit per-harness `--plan`, issue its signed approval, create its
one-shot enablement, then apply and verify it. `scripts/gds-exact-apply.sh`
performs the last three steps without weakening their separate durable state. Sync
refuses ambiguous input rather than guessing: `GDS_HARNESS_SELECTED_UNKNOWN` for a
selected id the catalogue does not know, `GDS_HARNESS_SELECTION_EMPTY` for a blank
selection, and `GDS_HARNESS_UNOBSERVABLE_NOT_PROVEN` for unselected entries whose
adapter could not be built. Pending work reports `GDS_HARNESS_SELECTION_DRIFT`.
See `docs/contracts/harness-adapters-v1.md`.

### `gds validate memories`

Validates semantic Serena memory names, strict metadata, committed versus
working-tree status, required body sections, safe repository-relative sources,
and deterministic source digests. It reports drift but never rewrites memory.

### `gds validate plan --file <path>`

Validates schema, expiry ordering, exact scope/precondition coverage, step
identity/scope, and the canonical plan digest. It neither stores nor applies
the plan.

### `gds state inspect [--path <state.db>]`

Opens an existing local state database in query-only mode and returns schema,
WAL/foreign-key settings, and bounded record counts. A missing database returns
`GDS_STATE_NOT_INITIALIZED` without creating any path.

### `gds session start --scope current --refresh none|origin`

Classifies the current and directly related Git boundaries. `none` is
read-only and reports cached remote evidence honestly. `origin` performs only a
strictly bounded, explicitly requested remote-tracking ref refresh, records
local ref mutation, detects forced updates by ancestry, and never integrates
the current branch or changes worktree/index content.

### `gds state initialize --plan|--apply <digest>|--verify <digest>`

Uses a deterministic self-hosting lifecycle plan because the operational state
store does not yet exist. Plan is side-effect-free. Apply requires the exact
plan digest and a bounded non-secret approval reference, fails closed under the
global mutation switch, creates a private schema-current database, and records
scope-bound digest evidence. Verify is read-only.

### `gds state migrate --plan|--apply <digest>|--verify <digest>`

Binds the current logical database digest and target schema. Apply rechecks the
digest under an exclusive SQLite boundary, creates and verifies a private
backup, runs ordered migrations atomically, and persists the exact plan and
approval-reference digests. Ordinary state opens never migrate implicitly.

### `gds operation inspect <operation-id>`

Opens the selected state store query-only and verifies the operation-to-plan
identity, ordered steps, append-only event digests, current locks, and effective
kill switches. It returns a conservative recovery classification and performs
no repair or mutation.

### `gds recover operation <operation-id> --plan|--apply|--verify`

Builds a conservative recovery decision from the immutable journal, exact
locks/fencing tokens, current Git/policy/manifest state, lease expiry, device
identity, and local PID liveness. Safe plans are stored and require exact
approval before a separate recovery saga applies. Active, remote, or
unknown-side-effect cases return a digest-bound manual-only plan. Verify accepts
the recovery operation ID, never the original plan ID.

### `gds identity new <kind>`

Generates one cryptographically random typed ULID in memory. It does not assign
or persist the identity.

### `gds validate estate`

Validates the private desired estate tree and cross-file references. The current
tree permits controlled pull-request mutations only for managed NDDev source
repositories; every other selector remains observe-only.

### `gds git topology`, `gds module inspect`, `gds validate gitlinks`

Inspect local Git topology and compare `.gitmodules`, gitlinks, initialized
submodule HEADs, typed relationships, and sanitized remote identities without
network access or Git mutation.

### `gds module verify [--module <name>] [--command-timeout <duration>]`

Runs each declared module's required verification lanes against a clean checkout
of the commit this repository pins, and reports per command whether it passed,
failed or timed out.

It is the one surface here that executes commands GDS did not write, and it is a
command rather than a validator for that reason. Every validator reasons about
the estate without running it, which is why a module could declare
`python3 scripts/validate_all.py` for as long as it liked after that invocation
stopped working: no amount of static reasoning reaches a `ModuleNotFoundError`
raised at import time.

Three properties make the answer meaningful:

- **The subject is the indexed gitlink**, not whatever the module's worktree
  currently holds. That is the commit this repository consumes; the worktree may
  sit on somebody's task branch, and verifying that would answer a question
  nobody asked.
- **The checkout is a throwaway worktree**, so the module's own checkout, branch
  and index are untouched and the command is safe to run while somebody is
  working there. It is removed on every path out, including failure.
- **Each command reports a bounded, redacted stderr tail.** A failing command may
  be a defect in the module or a tool missing from this device, and nothing in
  the exit code separates those. `GDS_MODULE_VERIFICATION_COMMAND_FAILED`
  therefore says the command did not succeed *on this device* and hands the
  reader the diagnostic, rather than asserting whose fault it is.

A module may declare `verification.commands.bootstrap`. It is selected first and
marked a prerequisite: it establishes what the required lanes need rather than
proving anything itself, which is why `schemas/v1/repository.schema.json` keeps
`bootstrap` out of `verification.required` and why a module is never reported as
verified by having been prepared. Nothing selected it before, so a module that
declared one got no benefit from it, and a module whose checks need preparation
had no way to say so -- `macos-ubuntu-bootstrap` requires `python3 -m pytest` on
a clean checkout with nothing installing pytest.

A failing prerequisite stops the module's remaining lanes and is reported as
`GDS_MODULE_VERIFICATION_BOOTSTRAP_FAILED`, separately from a failing check. The
statement is different: the checks were never attempted, which is not the same
as the module not passing them, and running them against an unprepared checkout
would produce failures describing the missing preparation.

Lane selection is otherwise static and reported without running anything:
`GDS_MODULE_VERIFICATION_UNDECLARED` when a module requires no lane at all,
`GDS_MODULE_VERIFICATION_LANE_EMPTY` when it requires a lane it declares no
command for, and `GDS_MODULE_VERIFICATION_LANE_UNKNOWN` for a lane outside the
schema vocabulary. A module that is not checked out is
`GDS_MODULE_VERIFICATION_NOT_PROVEN`, which is evidence GDS does not have rather
than a module that failed.

### `gds module coverage`

```bash
gds module coverage [--module <gitmodules-name>]
```

Compares what each declared module's anchor claims about its gate --
`verification.required_contexts` -- with the required status check contexts its
protected branch actually enforces, read from the provider.

This is the other direction of the drift `gds module verify` catches. Verify
answers "does the declared command still run"; coverage answers "does the
declaration still describe the gate". The second is the more dangerous side and
executing nothing reaches it: `agent-runtime` enforced `staticcheck` and
`govulncheck` as required checks while its anchor named neither, so anyone
reading the anchor as the contract ran a weaker check than the branch required,
and the module could have lost a required check without any tracked file
changing.

- `GDS_MODULE_REQUIRED_CONTEXT_UNDECLARED` -- enforced, not declared. The anchor
  understates the gate.
- `GDS_MODULE_REQUIRED_CONTEXT_UNENFORCED` -- declared, not enforced. The estate
  records assurance nothing produces.
- `GDS_MODULE_REQUIRED_CONTEXT_ABSENT` -- neither side names a check. A module
  with no gate at all is a fact worth stating rather than an empty agreement.
- `GDS_MODULE_COVERAGE_NOT_PROVEN` -- the module is not checked out, names an
  installation this runtime cannot read, or its rulesets could not be read.

The gate is read with two calls, because neither answers the question alone.
`repos/{owner}/{repo}/rules/branches/{default_branch}` reports which rules reach
the branch -- from repository and organization rulesets alike, with GitHub's own
condition matching already applied -- but carries no enforcement. The ruleset
list carries enforcement but not what reaches the branch. Only rules whose
ruleset is an `active` branch ruleset are counted: one in `evaluate` or
`disabled` enforcement reports check results without blocking, so counting it
would record a gate that lets a failing merge through.

Reconstructing this from ruleset documents alone does not work. An organization
ruleset id is a 404 on the repository endpoint, and every module in this estate
inherits one.

Context comparison is case-exact, because GitHub matches a required context
exactly and folding case would report agreement the provider does not honour.

Like `gds module verify`, this is a command rather than a validator and stays out
of this repository's own gates. A module understating its gate is that module's
defect; importing it into these required checks would block work here on a fix
somebody else has to make.

For this repository itself the same claim is checked locally and without the
network: `gds validate repository` compares `verification.required_contexts` with
the tracked `.github/rulesets/*.json` document that asks the provider for it
(`GDS_REPOSITORY_REQUIRED_CONTEXT_UNDECLARED`,
`GDS_REPOSITORY_REQUIRED_CONTEXT_UNENFORCED`). Since a separate reconciliation
already keeps that document and the provider in agreement, the anchor agrees with
the provider transitively. A repository that tracks no ruleset is not judged
there -- its gate is only knowable from the provider.

### `gds fork inspect`

Validates fork metadata and cached origin/upstream divergence. Cached refs are
never described as fresh.

### `gds github doctor`

Reports pinned GitHub provider capabilities and returns exit 3 until a secure
live installation runtime is explicitly configured and tested. It does not load
credentials or make an external request.

### `gds github inventory --installation <id> --runtime-config <path>`

Loads one private device-local runtime binding, mints a short-lived GitHub App
installation token through its declared secret backend, and reads the current
bounded installation inventory. It verifies that every repository belongs to
the estate account declared for that installation. It performs no provider or
state-store mutation; missing runtime evidence returns exit 3, authorization
failure returns exit 7, and an installation/account mismatch returns exit 12.

The token response must exactly match the installation permission and
repository-selection contract before the inventory request is sent. Missing,
extra, stronger, or differently scoped permissions return exit 12.

### `gds github governance --installation <id> --owner <owner> --repository <name>`

Reads one exact repository metadata/governance snapshot: merge and available
security settings, repository Actions policy, workflow-token defaults, and a
repository immutable-release state plus a bounded effective ruleset list. The
account is checked against canonical
installation intent before the provider request. The default result is
`observed-only`. `--compare-local` first proves that the local anchor has the
same immutable provider ID and owner/name, compiles its effective policy, and
returns typed `compliant`, `drift`, `observed`, or `ignored` field results.
The command is read-only in both inspection modes.

The same command exposes one explicit transaction grammar:

```text
gds github governance --plan \
  --installation <read-installation> --owner <owner> --repository <name> \
  --runtime-config <read-runtime> --device-id <device-id> \
  --session-id <session> --state-path <state-db>

gds github governance --apply <plan-id> \
  --installation <read-installation> --owner <owner> --repository <name> \
  --runtime-config <read-runtime> --mutation-runtime-config <write-runtime> \
  --device-id <device-id> --session-id <session> \
  --approval-ref <non-secret-ref> --state-path <state-db>

gds github governance --verify <operation-id> \
  --installation <read-installation> --owner <owner> --repository <name> \
  --runtime-config <read-runtime> --device-id <device-id> \
  --session-id <session> --state-path <state-db>
```

Planning always uses current read-App evidence and stores an immutable plan.
Apply additionally requires canonical `managed` assignment, enabled estate
mutation mode, matching mutation-capability scope, exact approval, and a
separate private mutation runtime. Stale full-snapshot evidence blocks before
write. Verify needs only the read runtime because it performs no provider
mutation. Canonical observe-only assignments deliberately return a policy block
before loading mutation credentials.

Public module policy manages `github.releases.immutable=true`. Governance
planning therefore emits a separate exact-digest step when a module repository
has immutable releases disabled; apply enables it through the repository
endpoint and verify re-observes the complete typed snapshot.

### `gds github projection-pr`

Publishes the exact current generated repository projection through one
provider branch and one draft pull request:

```text
gds github projection-pr --plan \
  --runtime-config <read-runtime> --device-id <device-id> \
  --session-id <session> --state-path <state-db>

gds github projection-pr --apply <plan-id> \
  --runtime-config <read-runtime> --mutation-runtime-config <write-runtime> \
  --device-id <device-id> --session-id <session> \
  --approval-ref <non-secret-ref> --state-path <state-db>

gds github projection-pr --verify <operation-id> \
  --runtime-config <read-runtime> --device-id <device-id> \
  --session-id <session> --state-path <state-db>
```

Repository installation, owner, name, provider ID, default branch, and
generated paths come from the committed local anchor and current provider
evidence; they are not caller-supplied mutation targets. Planning binds the
base OID, a deterministic `gds/projection-*` branch, expected blob states,
base-relative statuses, desired content digests, and exact draft-PR metadata.
An existing branch is accepted only when it is strictly forward from the same
base and contains no unexpected path. Apply evaluates canonical management,
lifecycle, capability, and mutation-mode gates before loading write
credentials. `--mutation-runtime-config` is rejected outside `--apply`.

### `gds reconcile --plan --runtime-config <path>`

Reads every exact estate installation with bounded concurrency and compares the
current provider inventory with selector-based desired classification. The
result is a side-effect-free reconciliation plan with
`external_mutations: []`; it neither stores observations nor applies drift
remediation. The runtime-config repository bound may be narrowed but never
expanded with `--max-repositories`.

### `gds report <scope>`

Read-only report scopes reuse canonical use cases instead of defining separate
policy logic:

```text
gds report estate-summary --runtime-config <path>
gds report drift --runtime-config <path>
gds report source-freshness [--as-of YYYY-MM-DD]
gds report harness-compatibility
gds report security
```

Estate reports perform the same bounded current reads as reconciliation and
emit `external_mutations: []`. The other reports project the canonical source,
harness, and security validators. Reports never remediate findings.

### `gds release candidate`

Builds a reproducible portable bundle in memory from a fully clean attached
Git source. It derives source commit/ref rather than accepting them as claims.
The result remains `NOT_PROVEN` because no artifact, attestation, SBOM, tag,
release, or external write is created.

### `gds release verify`

Validates one exact six-file release directory, independent local trust policy,
pinned offline trusted root, provenance and SPDX attestation bundles, current
CLI floor, target platform binaries, executable SBOM coverage, and the durable
anti-rollback acceptance state. The GitHub CLI runs with an isolated HOME and
without ambient GitHub credentials. The command is read-only and returns
`NOT_PROVEN` when any identity or offline evidence cannot be demonstrated.

### `gds release scope`

Resolves a canonical real installation root, independent trust domain,
scope digest, and current active release without mutation. This is the only
supported way to obtain the scope binding for a rollback authorization; callers
must not recreate the canonical digest algorithm manually.

### `gds release install|upgrade|rollback|remove`

Each local release lifecycle exposes exactly one mode:

```text
gds release <operation> --plan [exact inputs]
gds release <operation> --apply <plan-id> [same exact inputs] \
  --approval-ref <non-secret-ref>
gds release <operation> --verify <operation-id>
```

Install and upgrade require release, evidence, trust, and canonical install
roots. Rollback additionally requires an exact installed release key and a
schema-valid authorization bound to the canonical install-scope digest; apply
approval must equal the authorization approval reference. Remove targets only
the currently active accepted release.

Plan binds source HEAD, repository manifest, compiled policy, active release,
candidate digest, every installed file digest, target platform, and stored
authorization. Apply re-verifies all external inputs and preconditions before
mutation. Post-apply verification reads the stored operation and installed
immutable copies; new release identity flags are rejected. All filesystem
changes and acceptance records are journaled through the common durable
operation engine.

### `gds rollout plan --file <request>`

Validates an exact rollout request and returns a deterministic canary/wave plan
covering every stable repository ID once. It neither stores nor applies the
plan and performs no provider operation.

### Repository, module, fork, workspace, and portfolio lifecycles

The C5 command families are:

```text
gds repository onboard|rename|transfer|archive|delete
gds repository materialize|remove-checkout
gds module add|remove|update-pin|release|update-consumers
gds fork inspect|sync|detach|archive
gds workspace plan|audit|register-estate
gds portfolio plan
```

Local mutations use immutable stored plans and explicit apply/verify. Provider
transitions and unsupported package/network capabilities fail closed. Exact
preconditions and lifecycle semantics are defined in
`docs/contracts/lifecycles-v1.md`.

`gds workspace audit` accepts one or more explicit filesystem roots and one
schema-validated device descriptor. It discovers each Git boundary without
network access and classifies its physical mode:

- a standalone checkout must equal the device-selected
  `<workspace-root>/<provider-name>` target;
- an embedded submodule inherits its locator from the Git-reported
  superproject and exactly one typed `git-submodule-consumer` relationship;
- an embedded module is never reported as missing from its standalone
  portfolio root merely because it is checked out inside a superproject;
- missing anchors, ambiguous identities, unsafe relationship paths, absent
  superprojects, role mismatches, and real path drift fail closed.

The audit does not classify gitlink OID movement. Dependency pins remain the
authority of `gds validate gitlinks`, so placement and topology drift cannot
mask each other.

`gds workspace register-estate --plan|--apply|--verify` materializes the one
device-local XDG control-plane locator. The stored plan binds control-plane
HEAD, stable identity, repository-anchor digest, effective policy digest,
device/session identity, target path, previous file observation, and exact
candidate bytes. Apply requires scoped approval and uses the common operation
journal; verify is read-only.

## Read-only Git execution

The Phase 03 Git adapter permits only:

```text
git rev-parse
git status
git submodule status
git worktree list
git config --file .gitmodules --no-includes --null --get-regexp ...
git ls-files --stage -z
git remote / git remote get-url
git for-each-ref (bounded remote-tracking refs)
git rev-list --left-right --count (bounded remote-tracking refs)
```

Each command also has an exact argument-shape allowlist; a command name alone is
not sufficient. Remote names and repository paths cannot become options. The
adapter sets `GIT_OPTIONAL_LOCKS=0`, disables `core.fsmonitor`, disables terminal
prompts and pagers, caps stdout/stderr, uses argv arrays, kills the Unix process
group on cancellation, and redacts credential-shaped stderr before returning
it. Fetch, config mutation, checkout, merge, reset, clean, push, and all other
Git commands are rejected by the adapter.
