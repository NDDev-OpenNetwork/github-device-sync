# Publishing the five provider module releases

## Scope

The exact operational sequence for publishing `nddev-claude-app` 0.2.0,
`nddev-codex-app` 0.2.0, `nddev-grok-build-app` 0.3.0, `nddev-opencode-app`
0.3.0 and `nddev-pi-app` 0.2.0 through GDS.

This is an approval-gated operational program, not an implementation task. The
code contract it depends on is complete and verified on `main`:

| capability | where |
|---|---|
| required checks observed on the exact default-branch commit | `selectRequiredReleaseChecks` in `core/app/module_release.go` |
| tag convention declared, not assumed | `ReleasePolicy.TagStyle`, carried through the assessment and its drift comparison |
| assets pinned in the plan and re-verified before upload | `releaseAssetParameters{Path,Size,SHA256}` in `core/gitops/tag.go` |
| publication as one recoverable operation | `core/gitops/github_release.go`: a failed upload deletes the draft release rather than reporting partial success |

Publishing outside GDS -- `gh release create`, `gh api`, or a manual upload --
is not an acceptable completion of this program. It would produce releases whose
provenance the consumer cannot bind.

## Blocking prerequisites, in order

Each one is external to this repository and needs an explicit decision.

1. **A repository-selected GitHub Mutation App.** Until it exists, the runtime
   returns `GDS_GITHUB_MUTATION_RUNTIME_NOT_PROVEN` before any handler runs, so
   no plan can reach apply. The App stays repository-scoped and receives no
   operations beyond the release path.
2. **A device-local mutation runtime and private key** for that App, referenced
   from `~/.config/github-device-sync/github-mutation-runtime.yaml`.
3. **Actions enabled** on all five provider repositories. Their required check
   contexts cannot produce evidence while Actions is disabled, and the release
   plan fails closed on missing evidence rather than skipping the gate.
4. **Immutable releases enabled** on all five, so a published release cannot be
   silently replaced.
5. **Consumer pull requests and their checks re-verified** against the exact
   commits the releases will bind.
6. **Artifacts rebuilt and re-signed** against those exact commits. An artifact
   built before a commit moved does not describe it.

## Sequence once the prerequisites hold

For each of the five modules, in any order:

```bash
gds --json module release --plan --module <name> --version <semver> \
    --asset <exact-path> --device-id <device> --session-id <session>
gds --json operation approve <plan-id> --state-path <db> \
    --actor-id owner:example-user --actor-type owner \
    --key-id operation-approval-owner-2026 --private-key <pem> \
    --output <approval.json> --ttl 20m
gds --json operation enable <plan-id> --state-path <db> \
    --device-id <device> --session-id <session> --approval-file <approval.json>
gds --json module release --apply <plan-id> --device-id <device> \
    --session-id <session> --approval-ref <approval.json>
gds --json module release --verify <operation-id>
```

`GDS_TRUST_POLICY_FILE` must point at
`~/.config/github-device-sync/trust/operation-approval-trust-policy.json`, or
apply returns `GDS_APPROVAL_VERIFIER_UNAVAILABLE`. Plans expire in 15 minutes:
re-plan rather than reusing a stale id.

Unlike a repository-local projection write, this path keeps its signature and
one-shot enablement. It changes remote state that cannot be rolled back locally.

## Stop conditions

Stop and report rather than working around any of these:

- any required check is queued, in progress, skipped, stale, or ambiguous --
  only an unambiguous fresh `success` on the exact commit counts;
- an asset's bytes differ from the plan's recorded size or SHA-256;
- a tag, release or asset name already exists;
- an upload fails partway; the draft release is deleted and the operation is
  reported failed, never partially succeeded;
- the mutation runtime cannot be proven.
