# GDS Go core

Status: local fail-closed control plane. Local Git/filesystem workflows and
repository-bound GitHub governance, lifecycle, and projection publication use
plan/apply/verify.

The canonical estate declares `mutation_mode: "pull-request"` in
`estate/estate.yaml`, so provider writes are a controlled, approval-gated path
rather than a disabled one. What currently prevents a live provider write is a
missing prerequisite, not policy: the repository-selected Mutation App from
issue #98 does not exist, so the mutation runtime reports
`GDS_GITHUB_MUTATION_RUNTIME_NOT_PROVEN` before any handler runs. Estate rollout
is still plan-only by design -- `gds rollout` builds waves and does not apply
them.

## Package boundaries

```text
core/cmd/gds                         process entry point and only os.Exit owner
core/cli, core/app                   command parsing and use-case orchestration
core/domain, identity, manifest      portable identities and repository facts
core/serialization, validation       strict input and embedded schema contracts
core/context, discovery, inventory   local scope and observed-state inspection
core/estateregistry                  device-local trusted control-plane locator
core/estate, compiler, projections   desired state, policy, and candidates
core/workspace                       standalone and embedded device placement
core/githubruntime, secrets          private runtime bindings and secret adapters
core/skills, harness, memory         agent-system static contracts
core/providers/git, module, fork     read-only Git topology contracts
core/state, operations               local journals, locks, and saga primitives
core/providers/github, github*       bounded provider and typed write handlers
core/webhooks                        verified ingress and durable event queue
core/controller, reconciler          durable read-only controller service
core/audit                           pinned-key signed reconciliation evidence
core/bundle, rollout                 candidate trust and wave planning
core/assurance                       offline scale, security, restart, and budget evidence
```

Domain packages do not import Cobra or call `os.Exit`. External commands use
argument arrays, bounded output, cancellation, and explicit allowlists. The
Local materializers confine paths to one repository, reject symlinks and
unexpected drift, write atomically, journal operations, and verify exact
outputs. GitHub handlers bind one immutable provider repository, expected old
state, typed desired state, and read-after-write evidence. The canonical estate
blocks their apply paths before write credentials are loaded.

## Current command surface

Observation and diagnostics:

```text
gds context
gds status
gds discover
gds inventory
gds workspace audit
gds workspace register-estate --plan|--apply|--verify
gds doctor
gds state inspect
gds git topology
gds module inspect
gds fork inspect
gds github doctor
gds github inventory
gds github governance
gds github projection-pr --plan|--apply|--verify
gds reconcile --plan
gds report estate-summary|drift|source-freshness|harness-compatibility|security
gds-controller --runtime-config <private-file>
gds harness detect
gds harness eval
gds-assurance --root <control-plane> --output <new-report.json>
```

Static validation:

```text
gds validate schemas
gds validate repository
gds validate estate
gds validate context
gds validate gitlinks
gds validate git-state
gds validate projections
gds validate policies
gds validate skills
gds validate plugins
gds validate memories
gds validate harnesses
gds validate plan
gds validate security
gds validate visibility
gds validate public-artifact
gds validate absolute-paths
gds validate reproducibility
gds validate source-freshness
```

Candidate compilation and planning:

```text
gds identity new
gds compile policy
gds generate repository [--check]
gds generate repository --plan --device-id ... --session-id ...
gds generate repository --apply <plan-id> --approval-ref ...
gds generate repository --verify <operation-id>
gds skill package
gds harness install|update|rollback|remove --plan|--apply|--verify
gds harness sync|render|inspect|detect|doctor|eval|bridge
gds release candidate
gds rollout plan
gds source status
```

Compilation, packaging, release, and rollout commands return in-memory
candidates or plans. Repository generation mutates only in its explicit
transaction modes. Commands that need missing harness or provider evidence
return `NOT_PROVEN` rather than manufacturing a pass.

## Build and verify

```bash
go build -trimpath -o /tmp/gds ./core/cmd/gds
uv run --with-requirements requirements/test.txt --with pytest-cov python -m pytest
scripts/validate_go_core.sh --quick
scripts/validate_go_core.sh
scripts/validate_assurance.sh
```

Root pytest discovery is limited by `pytest.ini` to `tests/`; it never collects
tests from independent workspace repositories. The full Go validator
runs module integrity, vet, unit/integration/race tests, schemas, and CGo-free
cross-builds for macOS and Linux on arm64 and amd64. It requires the exact
source-registered release builder (`go1.26.7`). Quick validation may run on an
older local toolchain but leaves release evidence `NOT_PROVEN`.

`gds-assurance` is a separate release-gate binary. It requires a clean source
worktree and emits a schema-validated, source-commit-bound report for the
bounded 2000-repository C10 scenario. It never enables network or external
mutations.
