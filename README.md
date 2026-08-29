# GDS Agent Repository Estate

GDS is the agent-first control plane for the `example-org` and `example-user`
repository estate. It separates repository identity, logical
classification, Git topology, device placement, observed state, and agent
context so none of those concerns is inferred from a directory tree.

## Canonical model

```text
canonical source
  -> deterministic policy and context compiler
  -> immutable bundle and repository-local projections
  -> profiled agent harness adapters
  -> plan / approval / apply / verify / journal
```

- Reusable implementation lives in `core/`, `policies/`, `schemas/`,
  `skills/canonical/`, `harnesses/`, and `templates/`.
- Estate-specific intent lives in `estate/`.
- This repository owns its facts in `.gds/repository.yaml`.
- Generated files are `AGENTS.md`, `.claude/CLAUDE.md`,
  `.gds/compiled-policy.json`, `.gds/bundle.lock.yaml`, and the declared thin
  reusable-workflow caller under `.github/workflows/`.
- Device placement is declared in `estate/devices/`; workspace directories are
  locators, not Git parents or policy boundaries.
- Runtime observations and operation journals live under XDG state, never in
  tracked desired configuration.
- One device-local XDG registration binds every independent checkout to this
  control plane by stable repository ID and exact anchor digest; it is a
  locator, not a second policy source.

The former `nddev-monorepo`, `forks-monorepo`, and
`example-user-monorepo` metadata repositories are not part of the active
topology.

## Local development

```bash
GOTOOLCHAIN=go1.26.7 go build -trimpath -o /tmp/gds ./core/cmd/gds
GOTOOLCHAIN=go1.26.7 go build -trimpath -o /tmp/gds-codex-runtime-driver \
  ./core/cmd/gds-codex-runtime-driver
/tmp/gds --json context
/tmp/gds --json status
/tmp/gds --json validate
/tmp/gds --json generate repository --check
/tmp/gds --json workspace audit \
  --root "$HOME/Developer" \
  --root "$HOME/Desktop/github" \
  --device estate/devices/example-user-mac2.yaml
/tmp/gds --json workspace register-estate --plan \
  --device-id device_01JEXAMPZ00000000000000000 \
  --session-id device-bootstrap
```

The CLI is JSON-first and fail-closed. Read-only commands do not refresh or
integrate Git refs. Every enabled mutation uses an exact stored plan, scoped
approval evidence, precondition recheck, verification, and an append-only
journal.

`workspace audit` distinguishes standalone checkouts from embedded Git
submodules. Device selectors govern standalone targets; embedded modules are
validated against their Git-reported superproject and typed
`git-submodule-consumer` relationship instead of being misclassified as
top-level placement drift.

A device descriptor may declare an optional `class:` block
(`profile: desktop|server`, `gui`, `docker_mode`, `execution_policy`, and
server-only `hardening`) so the device intent and the OS installer it drives
cannot disagree. The phased bootstrap orchestrator
`scripts/bootstrap-device.sh` reads the class and drives the OS bootstrap, the
seed Go toolchain + `gds` build, and the control-plane staged commands in
order, with plan/apply gates at each boundary. See
`docs/runbooks/bootstrap-device.md` for the seam.

## Verification

```bash
scripts/validate_go_core.sh
scripts/validate_assurance.sh
scripts/validate_release.sh
uv run --with-requirements requirements/test.txt --with pytest-cov python -m pytest
```

`validate_release.sh` is intentionally stricter than the development gate: it
also runs the source-bound 2000-repository assurance scenario and requires
current source evidence before any artifact can be attested.

It does **not** prove harness runtime behaviour, and no longer claims to. The
seven harnesses are registered here with delegated runtime evidence; their
runtime suites live in the private `NDDev-it-com/setup-systems` repository,
which `harnesses/module-bridge.yaml` names as the evidence owner. Each profile
records `runtime_tests.last_result: delegated`, and
`gds validate harnesses --runtime` reports `runtime_evidence: "delegated"` with
that owner rather than a bare `not-proven` that reads like a failed attempt.
What the release gate does enforce is that the delegation is real: a harness
claiming `delegated` that the bridge does not map is rejected
(`GDS_HARNESS_RUNTIME_DELEGATION_UNDECLARED`), as is one that neither proves
evidence here nor delegates it (`GDS_HARNESS_RUNTIME_UNOWNED`). Promoting a
harness to `supported` still requires a local `pass`.

Architecture and contracts are in `docs/architecture/` and `docs/contracts/`.
