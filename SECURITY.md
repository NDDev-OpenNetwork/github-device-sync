# Security Policy

## Supported Versions

GDS ships immutable, tag-driven bundle releases. Only the current exact numeric
release receives security fixes.

| Version | Supported |
| --- | --- |
| Current exact tag `gds-v0.1.0` | yes |
| Older patch, minor, or major versions | no |
| `main` between releases | best effort |

## Reporting a vulnerability

Report suspected vulnerabilities privately through
[GitHub Security Advisories](https://github.com/example-org/github-device-sync/security/advisories/new).
Do not open a public issue for a confirmed vulnerability.

Include:

- affected command, package, and path (for example `core/githubruntime`,
  `gds module release`);
- the exact commit SHA or release tag, and `gds --version` output;
- host OS and architecture, and the Go toolchain version;
- minimal reproduction steps, observed behavior, and expected impact;
- redacted logs with no tokens, App private keys, cookies, or personal data.

You will receive an initial response as soon as reasonably possible. This
project is maintained on a best-effort basis and carries no SLA.

## Scope

In scope:

- the `gds` CLI and every package under `core/**`;
- policy compilation, bundle production, and projection generation
  (`policies/**`, `schemas/**`, `templates/**`);
- provider observation and mutation paths, including GitHub App credential
  handling and the runtime-config secret adapters;
- release production and consumption: SBOM, checksums, attestation issuance,
  and `gds` verification of downloaded artifacts;
- Git topology safety: submodule, worktree, and branch operations that could
  destroy or exfiltrate repository state.

Out of scope:

- vulnerabilities in the separately governed submodules under `modules/**` —
  report those to
  [`ci-workflows`](https://github.com/example-org/ci-workflows/security/advisories/new),
  or
  [`macos-ubuntu-bootstrap`](https://github.com/example-org/macos-ubuntu-bootstrap/security/advisories/new);
- findings that require an already-compromised local machine or an operator who
  has deliberately disabled the approval gates;
- missing hardening that no reachable code path can exercise.

## Posture of this repository

- **Mutations are gated.** External writes require explicit approval, and the
  provider mutation mode is disabled unless an operator configures it. Plans are
  produced, journaled, and verified rather than applied implicitly.
- **Secrets never enter canonical state.** `.gds/` and `estate/` declare identity
  and intent only. Tokens, App private keys, and provider observations live in
  operator-supplied runtime configuration outside the repository.
- **Public/private boundary is enforced by policy.** Repositories classified
  public must remain standalone and must never persist private parent context;
  generated projections carry the classification they were compiled under.
- **Full public OSS security suite runs on every push and pull request:** CodeQL,
  OSSF Scorecard, Dependency Review, gitleaks, Semgrep, OSV-Scanner, actionlint,
  and zizmor (SARIF), consumed from
  [`ci-workflows`](https://github.com/example-org/ci-workflows) pinned by full
  commit SHA.
- **Releases are immutable and attested.** Bundle releases ship an SPDX SBOM,
  `SHA256SUMS`, a build-provenance attestation, and an SBOM attestation, built
  inside the shared supply-chain workflow.
