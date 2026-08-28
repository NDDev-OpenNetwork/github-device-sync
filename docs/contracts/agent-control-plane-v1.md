# Agent control-plane contract v1

The normative lifecycle is:

`offline context → explicit refresh → immutable plan → signed approval → one-shot enablement → fresh CAS → write-set locks → ordered apply → verify → durable evidence → asynchronous OTLP export`.

The following invariants are fail-closed:

- No autonomous convergence and no desired-wins/live-wins conflict resolution.
- Approval binds actor, role, UTC validity, plan ID/digest, approval class, scope
  digest, and detached Ed25519 signature. External tickets are metadata only.
- Enablement authorizes one start of one exact plan on one device/session and is
  consumed in the same SQLite transaction as approval, journal, and locks.
- Cached evidence cannot satisfy a mutation precondition.
- Overlapping declared write sets conflict; disjoint sets may proceed. There is
  no last-writer-wins path.
- Dirty, ahead, or diverged Git state is reported; ordinary flow never auto-
  stashes, rebases, resets, force-pushes, or auto-pushes.
- Ruleset updates preserve all externally managed and unknown writable JSON. If
  full privileged observation or lossless representation is unavailable, write
  is refused.
- Stable/frozen bundle manifests bind a verified active-seven harness evidence
  manifest digest. Canary evidence gaps remain visible as provisional and cannot
  auto-promote.
- Operational identifiers may be exported. Credentials, signatures, private
  keys, raw source content, and secret-like values may not.

Recovery and audit use the local journal even when the telemetry sink is down.

## Offline approval UX

The approving public key is distributed in a non-secret `trust-policy/v1`
document and selected through `GDS_TRUST_POLICY_FILE`. Private Ed25519 keys stay
outside repositories and must be PKCS#8 PEM regular files with mode 0600.

```bash
gds operation approve "$PLAN_ID" \
  --state-path "$STATE_DB" --actor-id owner:example-user --key-id owner-2026 \
  --private-key "$PRIVATE_KEY" --output "$APPROVAL_JSON"
gds operation enable "$PLAN_ID" \
  --state-path "$STATE_DB" --approval-file "$APPROVAL_JSON" \
  --device-id "$DEVICE_ID" --session-id "$SESSION_ID"
GDS_MUTATIONS_DISABLED=false gds <workflow> --apply "$PLAN_ID" \
  --approval-ref "$APPROVAL_JSON" --state-path "$STATE_DB" \
  --device-id "$DEVICE_ID" --session-id "$SESSION_ID"
```

`--approval-ref` is retained as the 0.4 compatibility spelling but its value is
a file path, not an authorization string. A future major CLI may rename it to
`--approval-file`.
