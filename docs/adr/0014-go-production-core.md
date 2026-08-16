# ADR 0014: Implement the production GDS core in Go

Status: Accepted

Date: 2026-07-11

## Context

Phase 0 found a working Bash 3.2 and Python estate synchronizer with a
64-check parity suite. The target system needs typed domain boundaries,
deterministic JSON contracts, cancellable subprocesses, bounded concurrency,
SQLite, macOS/Linux/server distribution, and eventually one attestable
self-contained CLI for approximately 2000 repositories.

The local host has Go 1.26.4, Python 3.14.6, and Rust 1.96.1. Go 1.26.4 is
development-only evidence: the official Go vulnerability database identifies
standard-library vulnerabilities fixed in Go 1.26.5. The migration is
additive: no production stack choice is allowed to remove the legacy runtime
before parity gates pass.

## Decision

Implement the production `gds` CLI and portable control-plane core in Go.

- Set the module language baseline to Go 1.25 so the two currently supported
  Go release families can build it. Pin the release builder separately to the
  exact verified toolchain, initially Go 1.26.5. A builder older than Go 1.26.5
  is release-blocked; accepting a later builder requires a source-register
  update and the same release gates.
- Build release artifacts with `CGO_ENABLED=0` for macOS and Linux on amd64 and
  arm64. State-layer exceptions require a later ADR and cross-build evidence.
- Use the standard library for contexts, subprocesses, signals, JSON, HTTP,
  hashing, filesystem access, testing, and bounded concurrency.
- Invoke the installed Git executable through argument arrays and
  `exec.CommandContext`; do not replace Git plumbing with an in-process Git
  implementation.
- Use Cobra v1.10.2 only in the CLI adapter. Domain and application packages do
  not import Cobra, write directly to process-global streams, or call
  `os.Exit`.
- Use `github.com/santhosh-tekuri/jsonschema/v6` v6.0.2 behind a schema adapter
  for Draft 2020-12 validation with format assertions and local-only resource
  registration.
- Use `go.yaml.in/yaml/v4` v4.0.0-rc.6 behind a serialization adapter. The
  adapter rejects aliases, anchors, merge keys, duplicate keys, multi-document
  input, and ambiguous legacy booleans before domain decoding.
- Keep `scripts/validate_gds_schemas.py` as a temporary bootstrap oracle. It
  must become a thin wrapper or be removed after Go validator parity is proven.
- Defer the SQLite driver decision until the state-store phase. A pure-Go
  `database/sql` driver is preferred, but no dependency is selected without
  storage and cross-build tests.

The command architecture follows this boundary:

```text
cmd/gds
  -> internal/cli
     -> internal/app
        -> internal/domain
        -> internal/adapters/{git,manifest,schema,filesystem,github,state}
```

Only `cmd/gds` converts the final typed result into a process exit code.
Commands return one versioned result envelope and keep stdout machine-readable
when `--json` is active.

### Amendment — as-built layout

The layering above still holds, but the packages were never placed under
`internal/`. The shipped tree is `core/cmd/*` -> `core/cli` -> `core/app` ->
`core/domain`, with the adapter families as sibling `core/*` packages rather
than an `internal/adapters/` subtree; `core/README.md` carries the authoritative
package-boundary table. There are also now seven `core/cmd/*` binaries — `gds`,
`gds-assurance`, `gds-controller`, `gds-release-builder`, and the
`gds-{claude,codex,zcode}-runtime-driver` drivers — so the exit-code rule reads
as: each `core/cmd/*` main is the only place its process exit code is derived.

## Consequences

- GDS can ship as a small number of immutable platform artifacts without a
  Python or shell runtime dependency.
- Context cancellation, resource bounds, race testing, and cross-compilation
  become first-class build gates.
- The Go implementation must earn behavioral parity; current Bash behavior is
  not removed merely because an equivalent command name exists.
- Cobra, JSON Schema, and YAML are pinned supply-chain dependencies and enter
  dependency review, SBOM, and source-freshness gates.
- YAML v4 is still a release candidate. Its adapter boundary and fixture parity
  make replacement or final-version migration local, but API churn remains a
  tracked risk.
- The Python bootstrap validator temporarily duplicates execution, not policy:
  JSON Schemas and fixtures remain the only data-contract authority.

## Alternatives considered

- Continue in Bash: rejected for the typed graph, structured errors,
  concurrency, cancellation, and state-controller scope.
- Use Python for the production CLI: viable for migration speed, but rejected
  as the primary runtime because a reproducible single executable and
  cross-platform installation would require an additional packager/runtime
  supply chain. Python remains suitable for bootstrap and test tooling.
- Use Rust: technically strong, but rejected because its additional migration
  and contributor cost does not provide a material advantage over Go for this
  network-, Git-, and state-oriented controller.
- Use Go without a command framework: possible, but rejected because the target
  command tree is large and Cobra provides a mature nested parser. Its use is
  contained to prevent framework coupling.
- Use YAML v3: stable but frozen except for security fixes. The official
  maintainers recommend v4 for new work, so v4 is selected with explicit
  pre-release containment.

## Verification

- `go test ./...`, `go test -race ./...`, `go vet ./...`, formatting, and static
  analysis pass.
- Cross-builds pass for darwin/linux and amd64/arm64 with `CGO_ENABLED=0`.
- The full validation gate verifies the exact release-builder version before
  race tests or cross-build evidence can be accepted. Quick validation on an
  older local toolchain is explicitly development-only.
- CLI tests inject stdin/stdout/stderr, filesystem, clock, Git runner, and
  provider dependencies; domain tests never depend on process globals.
- Every command supports context cancellation and stable result envelopes.
- Go schema validation passes the same positive and expected-negative fixtures
  as the Python bootstrap validator.
- Black-box tests prove that read-only commands do not change Git refs, index,
  worktree files, provider state, or repository configuration.

## Rollback

The Go implementation is additive until parity and canary gates pass. Disable
or remove the new binary entry point while retaining schemas, fixtures, and the
legacy runtime. Do not delete legacy commands, manifests, or recovery evidence
as part of a stack rollback.

## Sources

- [Go 1.26 release notes](https://go.dev/doc/go1.26)
- [Go release policy and history](https://go.dev/doc/devel/release)
- [GO-2026-4970: os.Root symlink escape](https://pkg.go.dev/vuln/GO-2026-4970)
- [GO-2026-5856: crypto/tls ECH privacy leak](https://pkg.go.dev/vuln/GO-2026-5856)
- [Cobra repository and releases](https://github.com/spf13/cobra)
- [JSON Schema v6 package](https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6)
- [JSON Schema v6 releases](https://github.com/santhosh-tekuri/jsonschema/releases)
- [YAML organization Go implementation](https://github.com/yaml/go-yaml)
- [YAML v4 package](https://pkg.go.dev/go.yaml.in/yaml/v4)
