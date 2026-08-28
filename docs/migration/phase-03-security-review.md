# Phase 03 security review

## 2026-08-28 release-builder refresh

The stable 0.7.0 review reran `govulncheck v1.6.0` against the complete module.
Go 1.26.5 exposed reachable standard-library paths for GO-2026-6218,
GO-2026-6090, GO-2026-6089, GO-2026-5972, and GO-2026-5026. The exact release
builder and minimum security floor are therefore Go 1.26.7. Full, PR-required,
and release validation now execute the pinned vulnerability scanner instead of
relying on a historical evidence record. Device bootstrap verifies the official
archive SHA-256 before extracting the toolchain.

Status: implementation accepted for development; release evidence blocked.

Date: 2026-07-11.

## Scope

This review covers the additive read-only Go core introduced in Phase 03:

- strict JSON and YAML decoding;
- offline JSON Schema validation;
- local context and inventory resolution;
- read-only Git subprocess execution;
- CLI result and exit contracts;
- development and release validation gates.

It does not authorize a release, installation, provider access, GitHub
mutation, or legacy-runtime cutover.

## Threat model reviewed

- hostile repository paths, names, symlinks, and YAML content;
- shell and Git configuration injection;
- unbounded subprocess output, traversal, recursion, and concurrency;
- schema network resolution and regex denial of service;
- stale or falsely current remote state;
- duplicate repository identities;
- vulnerable build toolchain and dependency supply chain;
- accidental mutation by a nominally read-only command.

## Confirmed controls

- Git is invoked with argument arrays through `exec.CommandContext`; no shell
  interpolation is used.
- The Git adapter has an explicit read-only subcommand allowlist, disables
  optional locks and filesystem monitor hooks, suppresses prompts and pagers,
  caps output, and returns typed failures.
- Serialization rejects duplicate keys, aliases, anchors, merge keys,
  explicit tags, ambiguous legacy booleans, multiple documents, excessive
  input size, and excessive nesting.
- Schemas are embedded and registered locally; remote schema loading is
  forbidden.
- Discovery has explicit depth, repository-count, and concurrency bounds and
  rejects conflicting stable identities.
- Remote freshness remains `unknown` until an authoritative refresh exists;
  cached local refs are not presented as current provider evidence.
- Production packages do not call a shell, mutate Git, or call `os.Exit`; the
  process entry point owns exit conversion.
- A black-box matrix snapshots every regular file, symlink, directory, Git ref,
  index, configuration file, object, and worktree file in an isolated
  repository before and after every current command. The snapshots remain
  byte-identical and every result envelope reports no attempted mutation.

## Toolchain finding

The observed local builder is `go1.26.4`. The official Go release history says
that `go1.26.5`, released 2026-07-07, includes security fixes in `os` and
`crypto/tls`.

The official vulnerability database confirms:

- `GO-2026-4970`: affected Go 1.26 versions before 1.26.5; an `os.Root` path
  ending in a slash may follow a final symlink outside the root;
- `GO-2026-5856`: affected Go 1.26 versions before 1.26.5; Encrypted Client
  Hello handshakes may disclose pre-shared-key identities.

`govulncheck` v1.6.0 found no vulnerable symbols called by the current GDS
packages, but it reported both vulnerable standard-library modules in the
local toolchain. Absence of a current call path is not accepted as release
evidence for a control-plane binary.

## Decision

- `go1.26.5` is the initial exact release builder.
- `scripts/validate_go_core.sh --quick` may run on 1.26.4 but emits a warning
  and produces development-only evidence.
- `scripts/validate_go_core.sh` fails with capability exit code 13 before
  accepting race or cross-build release evidence when the builder is below the
  security floor or differs from the source-registered exact version.
- Changing the exact release builder requires source-register review and full
  validation; a newer unregistered toolchain is not accepted silently.

## Evidence

```text
go test ./...                                      PASS
go vet ./...                                       PASS
go test -race ./...                                PASS (development builder)
CGO_ENABLED=0 cross-build matrix                   PASS (development builder)
govulncheck v1.6.0 ./...                           0 called vulnerabilities
GO-2026-4970 / GO-2026-5856                       toolchain findings
scripts/validate_go_core.sh --quick                PASS with release warning
scripts/validate_go_core.sh                        expected BLOCKED (exit 13)
```

The race and cross-build results above prove source portability only. They do
not prove release safety because they were produced by `go1.26.4`.

## Residual risks

- Release build, SBOM, provenance, and consumer attestation remain
  `NOT_PROVEN` until the secure builder and bundle pipeline exist.
- Process-group termination requires additional platform-specific tests before
  long-running Git network operations are added.
- YAML v4 remains a release candidate and stays behind the serialization
  adapter with fixture parity gates.
- The current phase has no provider network access, state store, locks, hooks,
  or mutating operation engine; those controls must be reviewed when added.

## Primary evidence sources

- <https://go.dev/doc/devel/release>
- <https://pkg.go.dev/vuln/GO-2026-4970>
- <https://pkg.go.dev/vuln/GO-2026-5856>
