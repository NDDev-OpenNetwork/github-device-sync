# ADR 0019: Portable secret references and device-local GitHub runtime

Status: Accepted

Date: 2026-07-11

## Context

Estate desired configuration must work unchanged on macOS, Linux, and CI. A
reference such as `keychain:gds/...` incorrectly embeds one device backend in
portable intent. GitHub App IDs and provider installation IDs are operational
bindings, not reusable estate policy and not secrets.

## Decision

- Estate installation descriptors use stable logical references under
  `secret:gds/...`.
- A private device-local `github-runtime` document binds each exact logical
  installation to its GitHub App ID and provider installation ID.
- The same document maps the exact logical secret-reference set to one explicit
  backend: macOS Keychain, Linux Secret Service, environment, or private file.
- Runtime configuration contains no secret value and is never a tracked estate
  source.
- The loader requires a bounded regular non-symlink file with no group or other
  permissions and rejects missing or extra installations and secret mappings.
- Secret values are loaded only when a short-lived installation token is
  minted; they are not persisted or included in results and errors.

## Consequences

- Portable desired state no longer changes between devices.
- A device or CI runtime can select its native secret backend without changing
  repository history.
- Runtime configuration remains security-sensitive metadata and must be
  provisioned separately from the immutable public-safe bundle.
- Live GitHub access remains `NOT_PROVEN` until a concrete runtime file,
  credential, App installation, and effective permissions are inspected.

## Alternatives considered

- Encode `keychain`, `secret-manager`, or `github-actions` in estate references:
  rejected because it couples portable desired state to one runtime.
- Put private keys or tokens in runtime YAML: rejected because references, not
  secret material, belong in configuration.
- Infer provider installation IDs from account names: rejected because account
  names are mutable locators and do not prove App installation identity.

## Verification

- JSON Schema covers every supported runtime backend.
- Loader tests prove private-file and exact-set enforcement.
- Provider tests prove bounded App JWT minting, redacted failures, token cache,
  and installation isolation.
- Live verification is a separate read-only command and evidence record.

## Rollback

Remove the device-local runtime file and disable controller access. Estate
intent remains usable in observe-only mode; no provider state is changed by
loading or validating this configuration.
