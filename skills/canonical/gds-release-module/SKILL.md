---
name: gds-release-module
description: Use this skill only when the owner explicitly asks to release a module according to its declared pin and publication policy. Classify compatibility, verify the exact commit, publish required tag, package, or GitHub Release, and preserve provenance. Do not use it for module implementation or consumer pin updates alone.
disable-model-invocation: true
---

# Contract

Finalize and publish one module artifact under its declared compatibility and
publication contract.

## Use when

- A module change is complete and requires a policy-defined release.

## Do not use when

- The pin policy permits an unreleased default-branch commit and no release was
  requested.
- Consumer pins are the only requested change.

## Inputs

- Module repository ID, exact source commit, version impact, and release policy.
- Publication approval, read Installation App runtime, write credentials, and
  the exact bounded release asset paths required by GitHub release mode.

## Preconditions

1. Verify clean source, public API impact, and final commit OID.
2. Ensure every `verification.required` name has one completed successful
   GitHub Actions check on that exact commit; ambiguous or foreign evidence is
   not releasable.
3. Run `gds module release --plan --runtime-config <private-runtime>` and repeat
   `--asset <path>` for every intended GitHub release asset.
4. Obtain approval for tag, registry, release, and other external writes.

## Workflow

1. Recheck commit, version, checks, and publication endpoints.
2. Confirm the plan binds every asset basename, size, and SHA-256 digest.
3. Apply the immutable release plan. GitHub release mode must complete the
   draft-upload-publish sequence or prove draft cleanup.
4. Verify tag/package/release identities, exact asset inventory, and provenance.
5. Record consumer eligibility without updating consumers automatically.

## Stop conditions

Stop on version mismatch, failing checks, mutable or existing conflicting tag,
registry conflict, stale plan, missing provenance, or insufficient approval.
Also stop when an asset changes after planning or draft cleanup cannot be
proved; resolve the explicit recovery state before creating another plan.

## Verification

Run `gds module release --verify <operation-id> --json`.

## Output

Return source commit, compatibility classification, published artifacts and
digests, release policy evidence, and eligible consumer follow-up.

## References

No additional runtime reference is required; use the module's compiled release policy.
