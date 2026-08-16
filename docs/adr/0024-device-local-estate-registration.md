# ADR 0024: Resolve the control plane through a device-local estate registration

Status: Accepted

Date: 2026-07-11

## Context

Managed repositories are independent Git boundaries and are intentionally not
nested below the control-plane checkout. A session started in a project or
embedded module therefore cannot discover reusable policies by walking parent
directories. Requiring every harness or shell to export `GDS_ESTATE_ROOT`
would create duplicated device configuration and inconsistent startup
behavior.

## Decision

1. The default device locator is
   `${XDG_CONFIG_HOME:-$HOME/.config}/github-device-sync/estate-registration.json`.
2. The locator contains only the device ID, stable control-plane repository ID,
   physical checkout root, and exact `.gds/repository.yaml` digest. It is a
   locator, not desired-state or policy authority.
3. `GDS_ESTATE_ROOT` remains an explicit process-local override. Otherwise a
   control-plane repository self-resolves; every other repository uses the
   device locator.
4. Resolution verifies a regular non-symlink registration, closed v1 schema,
   canonical physical root, control-plane role, stable repository ID, and
   anchor digest before returning estate context.
5. `gds workspace register-estate --plan|--apply|--verify` is the only managed
   writer. Apply uses the common journal, scoped approval, stale-state checks,
   confined atomic materialization, and read-after-write verification.
6. Repository-local projections remain standalone. Losing the device locator
   makes central policy access `NOT_PROVEN`; it does not invalidate verified
   local repository facts.

## Consequences

- A session can start in any standalone project or embedded module and resolve
  one trusted control plane without copying global policy into parent folders.
- Moving or replacing the control-plane checkout requires a new exact
  registration operation; stale locators fail closed.
- Public repositories do not contain private device paths.
- Harness adapters consume one resolver result instead of maintaining their
  own estate pointers.

## Rollback

Restore the previous regular locator through a new approved registration plan,
or set an explicit process-local `GDS_ESTATE_ROOT` while repairing the device
registration. No repository projection or provider state is changed by this
rollback.
