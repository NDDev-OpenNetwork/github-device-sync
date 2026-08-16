# Bundle consumer v1 migration note

Status: required before the first immutable external GDS release.

## Change

The v1 consumer trust policy now requires:

```yaml
verification:
  trusted_root_digest: "sha256:<reviewed trusted-root.jsonl digest>"
```

The field is intentionally required. Accepting a custom offline root merely
because it arrived beside an attestation creates a circular trust decision.

## Migration

1. Obtain `trusted-root.jsonl` through the approved out-of-band GitHub/Sigstore
   process.
2. Review the source and exact bytes.
3. Compute SHA-256 and add the lowercase `sha256:` value to the independent
   local consumer trust policy.
4. Run `gds-release-builder --verify-trusted-root ... --trust-policy ...`.
5. Run the full schema, security, release, and consumer tests.
6. Distribute the trust-policy change before accepting a release built against
   the new root.

Root rotation is security-sensitive. It requires review, a new immutable GDS
release sequence, canary verification, and an explicit rollback target. Never
fall back to checksum-only verification or accept an unpinned custom root.

No automatic migration is provided because the digest is an external trust
decision, not a derivable repository default.
