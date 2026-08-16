# GDS GitHub read-only provider v1 contract

Status: isolated contract tests pass; live installation runtime is NOT_PROVEN.

## Authentication boundary

The provider accepts an injected GitHub App installation-token source. It does
not read PATs, environment variables, key files, or repository configuration.
Token values:

- are sent only in the Authorization header;
- must have more than 30 seconds of remaining lifetime;
- may use GitHub's variable-length token formats;
- never appear in result data or errors;
- are not persisted by the provider.

The token response must contain the exact canonical permission map and
repository selection. The current Inventory App accepts only repository
`actions`, `administration`, `checks`, `contents`, `metadata`, and
`pull_requests` at `read`, no organization permissions, and `all` repository
selection. Missing, extra, stronger, malformed, or differently scoped
permissions are rejected before any provider data request. Evidence contains
only sanitized expected/effective maps and never the token.

The device runtime can bind logical references to macOS Keychain, Linux Secret
Service, an explicit CI environment mapping, a private `0600` file, or the
`gh` CLI. The runtime configuration contains references only. GitHub App private
keys are read on token refresh, cleared from the immediate byte buffer, and never
persisted by GDS.

The `gh-cli` variant (ADR 0034) binds each installation to its declared GitHub
account instead of an App id. A `CLITokenSource` reads the token from
`gh auth token` through a sandboxed runner, then performs one bounded live
`GET /user` to inspect the real `X-OAuth-Scopes` header and the authenticated
account. The PAT's scopes are a coarse superset of the fine-grained installation
map, so the contract uses `superset` validation: every declared permission must
be granted at an equal or stronger level, and extra effective permissions are
tolerated. A PAT without the `repo` scope fails closed. Inventory for the
`gh-cli` model is enumerated through `/orgs/{login}/repos` (organization) or
`/user/repos` (user) — bare repository arrays — because a PAT cannot use
`/installation/repositories`.

## HTTP contract

- API version is pinned to `2026-03-10`;
- Accept is `application/vnd.github+json`;
- User-Agent is explicit;
- client timeout and response-size limit are mandatory;
- redirects are not followed;
- HTTPS is mandatory except explicit loopback test servers;
- pagination links must remain on the configured origin and endpoint;
- page size is 100 and inventory is bounded to 20 pages/2000 repositories;
- selected-action patterns are bounded to 100 unique values and normalized in
  lexical order;
- targeted governance rulesets are bounded to one 100-item page;
- duplicate IDs, changing totals, malformed identities, inconsistent
  visibility, and invalid GitHub URLs fail closed;
- response bodies are never embedded in API errors.

## Rate scheduling

Concurrency is partitioned by logical installation. The scheduler observes
primary remaining/reset headers and `Retry-After`, applies backpressure, and
never approaches GitHub's provider maximum by default. The read client does not
blindly retry; durable controller work owns retries.

## Discovery

`GET /installation/repositories` is the inventory root. Search is not used as
authority. Results are normalized and deterministically sorted by immutable
GitHub repository ID.

Targeted governance uses:

```text
GET /repos/{owner}/{repo}
GET /repos/{owner}/{repo}/actions/permissions
GET /repos/{owner}/{repo}/actions/permissions/selected-actions  # only when selected
GET /repos/{owner}/{repo}/actions/permissions/workflow
GET /repos/{owner}/{repo}/immutable-releases
GET /repos/{owner}/{repo}/rulesets?per_page=100&page=1
```

Repository metadata/merge/security state, Actions policy, workflow-token
defaults, repository-level immutable-release state, and effective
repository/organization rulesets are normalized into one typed snapshot.
GitHub's documented `404` for the immutable-release endpoint is normalized as
the disabled state only after the repository identity read has succeeded. A
second ruleset page is a bounded-limit failure rather than an implicit partial
result.

`gds github doctor` reports the pinned static contract and returns
`GDS_GITHUB_RUNTIME_NOT_PROVEN` without loading credentials or making a request.

`gds github inventory` is the explicit live read boundary. It requires a
private schema-validated runtime file, verifies the installation/account
identity, and returns current request IDs and rate metadata without persisting
the token or response. `gds reconcile --plan` performs the same current reads
for the exact estate installation set and emits no external mutation.

`gds github governance` reads one exact repository and defaults to
`observed-only`. `--compare-local` additionally proves that the current local
anchor identifies the same GitHub repository, compiles its canonical policy,
and returns deterministic field-level compliance and drift. These inspection
modes never apply remediation. Explicit `--plan`, `--apply`, and `--verify`
modes use the separate mutation capability and durable operation engine; the
canonical estate permits them only for managed NDDev source repositories and
still blocks every observe-only assignment before mutation credentials load.
