# Support

GDS is the agent-first control plane for the `example-org` and `example-user`
repository estate. Here is where to get help.

## Read the docs first

- **[README](README.md)** — the canonical model, repository layout, and how
  policy compiles into bundles and repository-local projections.
- **[`docs/`](docs/)** — architecture notes, contracts, and runbooks.
- **`gds <command> --help`** — every command documents its own flags, and
  `--json` emits a versioned result envelope suitable for scripting.
- **[CONTRIBUTING.md](CONTRIBUTING.md)** — the verification tiers and the rules
  that govern generated projections.

## Report a vulnerability — privately

**Do not open a public issue for security problems.** Report suspected
vulnerabilities through a private
[GitHub Security Advisory](https://github.com/example-org/github-device-sync/security/advisories/new).
See [SECURITY.md](SECURITY.md) for the policy and scope.

## Ask a question or report a bug

Open an [issue](https://github.com/example-org/github-device-sync/issues/new).
A useful report includes:

- the exact command you ran and its full `--json` output;
- the commit SHA or release tag, and your OS and architecture;
- what you expected instead, and whether the repository was clean;
- the output of `gds context --json` and `gds doctor --json` when the problem
  involves scope resolution, policy, or projections.

Redact tokens and private paths before pasting.

## Scope of support

This project is maintained by Danil Silantyev
([@example-user](https://github.com/example-user)), CEO NDDev, on a best-effort
basis. There is no SLA. Well-scoped, reproducible reports and pull requests that
follow the contribution rules get attention fastest.

Issues about the vendored submodules belong in their own repositories:
[`ci-workflows`](https://github.com/example-org/ci-workflows)
and [`macos-ubuntu-bootstrap`](https://github.com/example-org/macos-ubuntu-bootstrap).
