# C11-C12 local readiness and external execution plan

Status: local readiness proven; external mutations and acceptance gates remain
`NOT_PROVEN`

Observation time: `2026-07-12T00:18:50Z`

Authority: `docs/migration/gds-completion-plan.md`

## Publication update

The original control-plane migration PR and the onboarding PRs for
`nddev-ci-workflows`, `nddev-zcode-app`, `nddev-stroyme`, `example-user`, and
`example-harnesses` are merged. A follow-up evidence PR for `nddev-zcode-app` is
also merged. The exact published child PRs for Antigravity CLI, Claude Code,
Codex, MiMo Code, new-mac-or-Ubuntu, and OpenCode remain mergeable with green
checks but are blocked by independent review requirements. Codex auto-merge is
enabled; the other repositories do not permit auto-merge. No protection rule
or repository setting was bypassed.

`rldyour-ai-cli-tools` remains a green draft until the six review-gated child
commits reach their final `main` refs. Its final gitlinks have not been staged
prematurely. Source semantic baselines are now complete: all 57 records have
approved digests and all 57 current checks are unchanged. Runtime-qualified
source claims remain `NOT_PROVEN` until their named external evidence exists.

**2026-07-24 — first external immutable release published.** `gds-v0.1.0`
(source commit `bace996`) was built, attested, and published from
`refs/tags/gds-v0.1.0` by `.github/workflows/release-bundle.yml`, with keyless
Sigstore SLSA build provenance and an SBOM attestation over the six-file release
directory. Artifact attestation is available because the repository is public
and owned by the example-org organization. Harness runtime proof was delegated
out of the release gate (every `harnesses/*/profile.yaml` declares
`runtime_tests.required: false`), which is why publication proceeded while
`codex`/`zcode` runtime evidence stayed `not-proven`. This discharges the hosted
attestation and published-bundle blockers only; the E4/E5 canary, rollback, and
wave gates below are unchanged.

## Scope and local evidence

The device workspace contains exactly 14 managed Git boundaries. A source-bound
`gds workspace audit` classified seven as standalone checkouts and seven as
embedded submodules. All 14 are anchored and correctly placed; drifted and
invalid counts are zero. A filesystem scan of the declared `Developer` roots
and the retired `Desktop/github` root found no additional Git boundary.

The following gates passed before this plan was recorded:

- full Go validation and race tests with `GOTOOLCHAIN=go1.26.5`;
- integrated 2000-repository assurance with two installations and 1000 forks;
- 64-check quarantined legacy parity suite;
- 29-test root Python suite;
- `gds validate absolute-paths` and `gds validate public-artifact` across 728
  tracked files;
- deterministic projection checks for all 14 repositories;
- eight verified Serena memories;
- repository-native validation where the repository exposes a supported local
  command.

The source registry contains 57 volatile evidence records. All representations
are bounded, reproducible, and pinned by approved content digest. Runtime
evidence is not fully approved, so `gds validate source-freshness` remains
`NOT_PROVEN` for those qualified claims; this plan does not relabel them as
success.

## Exact local branch observation

`base` is the refreshed `origin/main` OID. `head` is the local task-branch OID
at the observation time. The control-plane head is evidence for this snapshot;
the external mutation plan must re-read it after this document is committed.

| Repository | Kind | Branch | Base | Head | Local state | Ring |
|---|---|---|---|---|---|---|
| `example-user/github-device-sync` | control plane | `feat/gds-control-plane` | `433c46b6923f7dc1efb96713b9ffc9330ca8ba58` | `43df6b9e3c2b3325aef1c8b6f2d7076f8965ea9e` | clean | A |
| `example-org/nddev-ci-workflows` | project/module | `task/gds-onboarding` | `e27d4e359ba9409e8d1ddd0f5021a5c67e38af75` | `134cca19b65e74e5b5ab6f30adfde08171efd451` | clean | A |
| `example-org/nddev-zcode-app` | module | `task/gds-onboarding` | `7df8f944f53ce8036472d3d308af8e8e0cf8baaa` | `eea680dfdc567ab737422dd1db9dbd102baace49` | clean | A |
| `example-org/nddev-stroyme` | project | `task/gds-onboarding` | `29fafe83cd400cc4b2481cae50fca7278a9c9221` | `3ed3863aadf369cea83d1cca937844d54a16097b` | clean | A |
| `example-org/rldyour-antigravity-cli` | module | `task/gds-onboarding` | `656da46b70cb5b94ae5dfccb0541c0cc11b1748e` | `a770c7e430ecfad0530f32d6031e6979002a63ba` | clean | A |
| `example-org/rldyour-claudecode` | module | `task/gds-onboarding` | `7c2ec4ed669ff8d2424d9e5a65f8329092b32cd7` | `24010794fe82a8b4a97d4b95ff355c7a6a6abcdf` | clean | A |
| `example-org/rldyour-codex` | module | `task/gds-onboarding` | `c34dd389b6d875533f09e60d9273359ba0044a4b` | `2a19f446121e5cde5fc48eb8d7c7c6b00a53b918` | clean | A |
| `example-org/rldyour-mimocode` | module | `task/gds-onboarding` | `a12d3995c9964da8c8f8e70e24d0c66fd71188c3` | `f7e4664c2d269f8888601133ed38ee1e2556f650` | clean | A |
| `example-org/rldyour-new-mac-or-ubuntu` | module | `task/gds-onboarding` | `0a6b3cca35cdbc13947b3acec195204072248f91` | `a45564d9bb4d3eb16995c203360a1d74f1cd96f3` | clean | A |
| `example-org/rldyour-opencode` | module | `task/gds-onboarding` | `fa4fdde904f0c7db82542c4740a5cd491f33cb9e` | `db448a55babef0d890bbd8fed4d8b8d08966639e` | clean | A |
| `example-user/example-user` | docs | `task/gds-onboarding` | `669a40bdf6c73cb0917e4f145e83626e7f9b37c1` | `6949c650976f3641c7793d73a5a480f9174ed162` | clean | A |
| `example-org/example-harnesses` | project/superproject | `task/gds-onboarding` | `04ad9fedff4d9dbc8c3cd2991dc4c944fb7fd7c6` | `bfedc8d212917b0c3cf86348dd26adf06f81b3dc` | child worktree off gitlink | B |
| `example-org/rldyour-ai-cli-tools` | project/superproject | `task/gds-onboarding` | `f2bed13a5e4e856e29bdb8af454f3f8871241b6a` | `e1cda24840f68e1da9fcab88ef1d2a3f14f712f7` | six child worktrees off gitlink | B |

All branches are zero commits behind their refreshed bases. Each onboarding
branch is exactly two commits ahead. The control-plane branch was 201 commits
ahead at observation time and has no upstream. Neither superproject has a
staged gitlink change; its reported dirt is the expected consequence of an
embedded child being checked out on an unpublished task commit.

## External dependency order

### E0 — exact re-observation

Immediately before any write:

1. fetch only the relevant `origin/main` and target task refs without prune;
2. re-read every base, head, worktree/index fingerprint, policy digest, and
   projection digest;
3. reject the plan if any value differs;
4. store one bounded external plan with an expiry, approval class, exact
   repository IDs, refs, OIDs, and action set.

### E1 — ring A publication

After an exact A2/A6 approval:

1. publish the 12 ring-A task branches without force;
2. verify each remote task-ref OID equals the approved local head;
3. open one draft pull request per Git boundary against `main`;
4. do not enable auto-merge, change repository settings, or advance ring B;
5. journal provider request IDs, remote OIDs, PR URLs, and any partial result.

Failure in one repository stops that repository and prevents automatic ring
advance. It does not rewrite or delete already published branches.

### E2 — hosted checks and child integration

Current CI, required checks, reviews, and merge eligibility must be observed on
the exact PR heads. Merges require a separate exact approval. After each child
merge, fetch `main` and record the resulting OID; never assume that a squash or
rebase merge preserves the task-branch OID.

### E3 — ring B superproject pins

Only after every selected child commit is reachable from the approved final
child ref:

1. move each embedded child worktree to the merged child `main` OID;
2. update only the corresponding superproject gitlinks;
3. run gitlink, projection, repository-native, visibility, and security checks;
4. commit the exact pins on the existing parent onboarding branch;
5. publish and open the two parent draft PRs through a new exact plan;
6. verify the parent PR checks before any merge approval.

The current off-gitlink worktrees must not be staged as final pins because the
task commits are not yet published or main-reachable.

### E4 — C11 runtime canary and rollback

C11 acceptance additionally requires exact clean-session evidence for all
applicable harness profiles, current source-freshness evidence, live GitHub
read/write capability separation, an immutable canary bundle, and one proven
rollback. Static adapter files or locally detected binaries are insufficient.

### E5 — C12 waves and legacy retirement

C12 starts only after C6-C11 acceptance. Each wave receives its own approval,
cursor, failure gate, and reconciliation report. The three retired metadata
repositories remain only as remote archive-branch recovery evidence. Their
working directories and root gitlinks are already absent. Quarantined legacy
engine files and residual local Git metadata are removed only after the final
parity and retention gates, never as part of ring-A publication.

## Current blockers

- all seventeen native harness behavioral evidence records remain `NOT_PROVEN`;
- `cursor-cli`, `kimicode`, and `pi` are not installed; the observed Grok
  wrapper cannot resolve a runtime;
- no live Inventory App or Mutation App credential/permission evidence exists;
- hosted attestation and a published immutable bundle now exist (`gds-v0.1.0`,
  2026-07-24; see the publication update above); no Linux consumer rehearsal,
  managed-repository rollback, or canary PR evidence exists;
- source semantic baselines are complete; runtime-qualified source claims keep
  the release source-freshness gate `NOT_PROVEN` until exact evidence exists.

None of these blockers invalidates the local implementation or workspace
cutover. Each blocks only the acceptance stage that requires the missing
external or runtime evidence.
