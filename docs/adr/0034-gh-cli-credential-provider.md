# ADR 0034: gh CLI credential provider and permission superset contract

Status: Accepted

Date: 2026-07-29

## Context

ADR 0019 binds each estate installation to a device-local GitHub App identity
and an explicit secret-manager adapter. ADR 0021 requires the installation token
to carry the exact declared permission map, so an over-privileged App cannot
silently operate. Together they make the GitHub App installation token the only
credential model and keep live access `NOT_PROVEN` until a private key, App
installation, and exact effective permissions are inspected.

A device owner may prefer to drive GDS from the already-authenticated `gh` CLI
instead of provisioning and rotating a GitHub App private key. The `gh` CLI
holds one personal access token (OAuth) for one GitHub account. That token's
coarse scopes (`repo`, `read:org`, `workflow`) are a strict superset of the
fine-grained installation permission map, and a PAT does not report an
installation-scoped permission map or repository selection. The exact-match
contract from ADR 0021 therefore rejects the PAT before any request.

## Decision

- GDS admits a second, first-class credential model: the `gh-cli` secret-store
  provider, and a matching `CLITokenSource` in the GitHub provider.
- The `gh-cli` variant binds each estate installation to its declared GitHub
  account (`account_login`, `account_type`) instead of an App id and provider
  installation id. The device-local `github-runtime` document selects the
  variant; the portable estate intent is unchanged.
- A `CLITokenSource` reads the token from `gh auth token` through a sandboxed
  runner, then performs one bounded live `GET /user` to inspect the real
  `X-OAuth-Scopes` response header and the authenticated account. The scopes
  are mapped to the fine-grained permission vocabulary conservatively; a PAT
  without the `repo` scope fails closed because it cannot read repository
  metadata.
- A new permission contract mode, `superset`, requires the token to grant at
  least every declared permission level (read <= write) over the declared
  selection. Extra or stronger effective permissions are tolerated; missing or
  weaker declared permissions still fail closed. The exact-match contract
  remains the default and the only mode for GitHub App installations.
- Inventory adapts to the credential model. The App installation token lists
  repositories at `/installation/repositories` with a `{total_count,
  repositories}` envelope. A PAT cannot use that endpoint and is enumerated
  through `/orgs/{login}/repos` (organization) or `/user/repos` (user), which
  return a bare repository array with no total count.
- Read and mutation remain separate concerns. The mutation runtime's
  `gh-cli` separation is structural rather than by App identity: both runtimes
  must use the `gh-cli` provider, every mutation capability must target a
  declared read installation, and the mutation client exposes only its declared
  write operations. The canonical estate still gates management assignment and
  `mutation_mode` before any apply.

## Consequences

- A device can observe and reconcile its estate through the `gh` CLI without a
  GitHub App private key, while the exact-match App contract and its
  over-privilege protection remain available unchanged.
- Permission evidence under `superset` reports `verified-superset` with the
  real effective scopes, so the attested evidence is observed truth, not a
  declared assertion.
- A PAT's scopes are broader than the estate contract; that breadth is now
  explicit and tolerated rather than hidden behind an exact map.
- The `gh-cli` model is a single principal and cannot provide ADR 0023's
  distinct-App-identity separation; separation is enforced at the capability
  boundary instead.

## Alternatives considered

- Relax the exact-match contract globally so any valid token is accepted:
  rejected because it removes the over-privilege protection for the App model.
- Synthesize the installation permission map from the declared contract instead
  of inspecting the live scopes: rejected because the evidence would no longer
  be observed truth and a weakened PAT could be reported as full privilege.
- Treat `gh-cli` as a secret-store adapter only (returning the raw token):
  rejected because the PAT has no installation permission map, so the token
  source must also inspect scopes and select the inventory endpoint.

## Verification

- `core/providers/github/ghcli_token_test.go` covers token minting from scopes,
  cache, scope-to-permission mapping, missing-scope rejection, and the full
  account-inventory read path.
- `core/providers/github/permissions_superset_test.go` covers superset
  acceptance of stronger/extra permissions and rejection of missing/weaker
  declared permissions.
- `core/githubruntime/build_ghcli_test.go` proves the `gh-cli` runtime builds
  superset clients that enumerate the account list endpoints.
- A schema fixture (`valid-github-runtime-ghcli.yaml`) exercises the
  `gh-cli` variant through the canonical schema validator.

## Rollback

Remove the `gh-cli` secret-store variant and the `CLITokenSource`, revert the
`runtimeInstallation` schema to the App-only form, and drop the `superset`
permission mode. The exact-match App model and the estate intent are unaffected;
no GitHub state changes are required because this stage adds a credential
provider, not a mutation path.
