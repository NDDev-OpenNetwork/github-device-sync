# GDS repository estate lifecycles v1 contract

Status: local repository, module, fork, workspace, and portfolio lifecycle
contracts are implemented on the durable plan/apply/verify engine. Operations
that require GitHub repository mutation, network push, package registries, or a
GitHub Release remain explicitly unavailable until their dedicated providers
are accepted.

## Shared transaction boundary

Every mutating local lifecycle uses an immutable stored plan, exact actor and
approval scope, stale-state rechecks, fenced repository locks, bounded fixed
argv execution, append-only journal evidence, and explicit verification.
Validators and aggregate planners never repair state implicitly.

Stable GDS repository identity never derives from owner, repository name,
remote URL, local path, branch, device, portfolio, or role. Each discovered Git
history remains an independent mutation boundary.

## Repository lifecycle

```text
gds repository onboard --plan|--apply|--verify
gds repository rename --plan|--apply|--verify
gds repository transfer --plan|--apply|--verify
gds repository archive --plan|--apply|--verify
gds repository delete --plan|--apply|--verify
```

Onboarding accepts one schema-validated candidate `.gds/repository.yaml` and
requires a clean attached branch or a clean embedded detached checkout whose
HEAD exactly equals its stage-zero superproject gitlink. A default branch must
be current with `origin/<default>`. A task branch may be intentionally
unpublished, or it must be current with its same-name origin upstream. For the
embedded detached case, the superproject must also be clean. The workflow
verifies provider identity against the configured origin, compiles the
candidate policy, and materializes the missing anchor atomically. It cannot
replace an existing identity or accept an arbitrary detached checkout.

Rename, transfer, and archive plans preserve the stable repository ID and
alias history. They place the provider transition before the local anchor
change. Rename and archive have fixture-proven installation-token handlers;
the canonical estate permits live apply only for managed NDDev source
assignments; observe-only assignments remain blocked before mutation
credentials load.

Transfer remains deliberately fail-closed. GitHub requires a GitHub App user
access token, returns `202 Accepted` with the original owner, and completes the
transition asynchronously. GDS validates and stores the intended transfer but
marks it not ready for apply until a dedicated user-token and asynchronous
acceptance adapter exists. It never updates only the local remote or anchor.

Deletion turns on whether the repository is *finished*, not on whether one
checkout is clean, and those are different questions.

The preconditions the planner always established are real and unchanged: an
archived anchor, exact stable and provider identity confirmations, complete
relationship analysis, no remaining consumers or outgoing relationships, no
unanchored boundary in the selected analysis root, a clean attached
default-branch checkout, and a signed approval of an exact plan. All of them
observe the repository's *placement*. None observe its *contents*.

`RepositoryRetirementEvidence/v1` observes the contents. It is built from
complete local enumeration and complete paginated provider observation, and it
classifies every item into exactly one of four states:

- `completed` — the work landed and survives the deletion;
- `preserved` — it does not survive, and the operator named that exact identity
  with `--preserve`;
- `blocking` — unfinished work the deletion would destroy;
- `unknown` — it could not be observed.

**Unknown blocks.** A page that could not be read, a refused permission and a
rate limit are all indistinguishable from "nothing found", and only one of those
readings is safe in front of an irreversible mutation. Every collection carries
a read flag for exactly that reason: an empty list and an unread one are the same
bytes and opposite meanings.

What is enumerated: every worktree over the repository's Git store; every local
branch and tag, and whether each is published; the count of commits reachable
from a local ref and from no remote-tracking ref; every provider branch; every
pull request in every state, with merged distinguished from closed-unmerged,
because GitHub reports both as `closed`; every open issue; and whether any review
conversation is unresolved. Review-thread resolution is not in the REST surface
and is asked over GraphQL, since an unresolved conversation on a merged pull
request is exactly the kind of unfinished work a retirement decision must not
step over.

Preservation is declared per identity with a repeatable `--preserve`, never as a
blanket override: one flag waving past everything at once would restore the
behaviour this evidence exists to remove. An `unknown` can never be preserved
away — a declaration says "I know about this and accept losing it", and nobody
can say that about something they did not observe.

The evidence claim is digested into the plan precondition alongside the provider
state, and the observer rebuilds it immediately before the mutation. A branch,
pull request, issue, review conversation, worktree or local ref that appeared
since planning yields a different digest and the engine refuses with nothing
attempted. The digest covers which identities were observed and what each was
found to be — not the counts or the prose, which re-render differently on every
observation and would make every plan stale before its handler was called.

Independently of the evidence, every worktree over the repository's Git store
other than the one the command runs in blocks with
`GDS_REPOSITORY_DELETE_SECONDARY_WORKTREE`, carrying that worktree's path,
branch, head and observed cleanliness, or `status: unreadable` when it cannot be
inspected. `status.Worktrees` is bound into the plan precondition, so a worktree
created after planning invalidates the stored plan rather than being ignored.

## Module lifecycle

```text
gds module add --plan|--apply|--verify
gds module remove --plan|--apply|--verify
gds module update-pin --plan|--apply|--verify
gds module release --plan|--apply|--verify
gds module update-consumers --plan
```

Add and remove change one exact typed `git-submodule-consumer` relationship and
preserve all unrelated anchor content. Add requires `.gitmodules`, the index
gitlink, provider identity, and module stable identity to agree. Remove is
blocked while the gitlink contract still exists.

`update-pin` accepts one non-default consumer task branch and one exact
stage-zero gitlink whose checkout is either absent or already at the target
commit. The selected module must match the typed relationship, be clean on its
default branch, and have that exact commit published on its origin. The current
handler supports `default-branch-commit`; version and package policies require
their release providers first.

Accepting the second checkout shape is what makes the command usable. The
consumer is otherwise clean, but an advanced submodule reports its gitlink as
one unstaged change, and the cleanliness rule counted that as a dirty consumer
-- while the eligibility rule demanded a *changed* gitlink. The two read as a
contradiction, and the only way through was `git submodule deinit`, written down
nowhere. A checkout sitting at the target commit is stronger evidence than an
absent one: the consumer holds the commit it is about to pin. The relaxation is
exactly one gitlink wide; a staged, untracked or conflicted path, or a second
unstaged one, still refuses with `GDS_MODULE_PIN_CONSUMER_STATE_UNSAFE`.

`update-pin` runs the module's required lanes twice -- once when planning at
the target commit, and once when the engine re-observes its preconditions before
the mutation -- so it defaults to a twenty-minute deadline rather than the
two-minute one sized for a read. An explicit `--timeout` always wins. Before
that, the default expired mid-observation and the engine reported
`GDS_STALE_PLAN`, "the repository changed before its first mutation step", which
sends the reader looking for a concurrent writer that does not exist. Any
command whose deadline expires now also carries
`GDS_COMMAND_DEADLINE_EXCEEDED`, naming the deadline and the flag.

Applying a pin needs no approval. The only mutation is a gitlink rewrite in the
consumer's own working tree: it writes no provider, replaces no credential and
publishes nothing, and the consumer's pull request and checks are its real gate.
Requiring a signed approval meant the private signing key had to be present to
advance a pin, so pins stopped advancing and the estate drifted behind modules
it had already merged. Signed approval stays on the operations that write
outside the repository -- provider lifecycle, rulesets, releases and anchors.

The module's origin is observed and never written: the only mutation is a
gitlink rewrite in the consumer index. Both plan and apply once gated that
observation on push capability to the module's remote, which refuses every
remote that is not a local path -- so on a real estate the pin could not advance
for any module, whatever its state, while the fixture-backed test passed against
a local remote.

A module's required checks must be proven at the target commit, not assumed.
Planning runs its required lanes there, the way `gds module verify` does, and
`GDS_MODULE_PIN_CHECKS_NOT_PROVEN` now means a lane did not pass rather than
that the module declared one at all. The verification result joins the plan
fingerprint, so an approval cannot outlive the green run that justified it.
Planning therefore executes the module's declared commands; it still writes
nothing, in the consumer or the module.

A `git-submodule-consumer` relationship may set
`pin_management: consumer-transaction` when advancing the gitlink also requires
repository-owned mirrors, inventories, attestations, or other atomic evidence.
GDS then fails closed with `GDS_MODULE_PIN_CONSUMER_TRANSACTION_REQUIRED` and
does not stage the gitlink. The consumer's canonical transaction owns that
mutation; GDS remains the topology and placement authority.

`release` does not infer publication from pin policy. `release.mode` is the
independent authority: `none` publishes nothing; `package-version` blocks until
a registry provider exists; `github-release` uses the implemented GitHub Release
provider and is gated by the ordinary
mutation-mode, capability, and credential chain like any other provider
mutation; and `version-tag` creates one immutable `v<semver>` tag only
when the clean default commit is exactly published, the tag is absent locally
and remotely, required checks are proven, and module publication policy does
not require the unavailable GitHub provider.

GitHub release planning requires one to sixteen explicit `--asset` paths. Each
asset must be an owner-controlled, non-symlink, non-group/world-writable regular
file of at most 64 MiB; the complete set is limited to 128 MiB and duplicate
basenames fail closed. The plan binds absolute path, basename, size, and
`sha256:` digest. Apply re-reads those exact bytes, creates a draft release,
uploads the complete set, then publishes it immutable. Any failure before
publication triggers draft deletion; a failed cleanup is reported as an
explicit recovery requirement and never as success. Immediate verification
re-observes the final release and asset inventory through GitHub. Later
standalone verification validates the durable exact after-evidence without
requiring mutation credentials.

Module release policy may set `release.tag_style` to `semver` for an existing
numeric SemVer release line. Omitted or `v-semver` preserves the default
`v<semver>` contract. The exact style is captured in the immutable plan; apply
cannot reinterpret a stored version under a different tag convention.

Required release checks are observed through the repository's read Installation
App on the exact published default-branch commit. A required name must resolve
to exactly one completed successful GitHub Actions check; duplicate names,
foreign apps or repositories, nonterminal/failed/skipped/stale conclusions, and
evidence for another commit fail closed. The immutable plan records the check,
Actions app, run/job ids, completion time, details URL, and exact commit SHA,
and apply re-observes the same evidence before any publication mutation.

Which provider contexts are required comes from `release.required_checks`, which
names provider check-run contexts directly. It takes precedence over
`verification.required`, which names local command lanes (`test`, `lint`) and
resolves as a provider context only because the active module repositories
publish a check run named after their required lane. Declare
`release.required_checks` for any repository whose check is named outside that
lane vocabulary — `ci-gate`, `CodeQL / CodeQL (go)` — because such a context
cannot be expressed through the lane list at all.

`verification.required_contexts` is a third, separate list and is not a release
input. It states which contexts the repository's protected branch enforces, so
`gds module coverage` has a claim to compare with the provider. A required
context is a check run name and not a command; nothing derives one from the
other, which is why it is stated rather than inferred from the lanes beside it.

### Private QA evidence for a module release

A public module carries no QA of its own: the lanes that decide whether a
version is releasable live in the private `example-harnesses` control plane, and
their results must not be republished into a public repository. A module mapped
to an active harness in `harnesses/module-bridge.yaml` therefore cannot be
released without a signed harness evidence record, and planning fails closed
with `GDS_MODULE_RELEASE_HARNESS_EVIDENCE_REQUIRED` when one is not supplied.

The record is the same artifact the harness already produces for bundle gating
(`scripts/gds_evidence_bundle.py` in `example-harnesses`), so the estate keeps one
evidence system rather than two. Supply it with:

```bash
gds module release --plan \
  --version X.Y.Z --asset <path> \
  --harness-evidence <directory containing <harness-id>.json> \
  --harness-evidence-trust <trust policy JSON>
```

The record is verified against this estate's own `module-bridge.yaml` and
`harnesses/<harness-id>/profile.yaml` digests, its signature against the trust
policy, and its `module_sha` against the exact commit being released. A pass for
a different revision, an expired record, a failed lane result, or a tampered
payload each fail closed. What is bound into the immutable plan is the harness
id, harness root SHA, module SHA, executable version, suite version and cases
digest, evidence digest, and validity window — so an approved release cannot be
separated from the QA that justified it, and apply re-observes the same proof.

A module with no active bridge mapping — a shared CI or bootstrap module — has
no harness and is gated by its declared provider checks alone.

Selected consumer planning requires exact stable consumer IDs and a complete
relationship index. Each eligible git-submodule consumer receives an
independent stored pin subplan. Unsupported package consumers and individual
repository failures remain visible without erasing eligible subplans.

## Fork lifecycle

```text
gds fork inspect
gds fork sync --plan|--apply|--verify
gds fork detach --plan|--apply|--verify
gds fork archive --plan|--apply|--verify
```

Fork sync is fast-forward-only through the isolated local provider. Origin and
upstream identities, exact old/new OIDs, clean attached state, and fork policy
are rechecked. Maintained fork commits are never discarded. There is no force
sync path and no hidden reset fallback.

Detach removes only the exact credential-free `upstream` remote, then
materializes a candidate anchor that preserves upstream identity history while
changing policy to detached. Archive reuses the provider-first repository
transition and remains blocked until C8.

## Device workspace lifecycle

```text
gds workspace plan
gds repository materialize --plan|--apply|--verify
gds repository remove-checkout --plan|--apply|--verify
```

Placement derives from the portable device descriptor, repository portfolio,
provider name, and current environment. It never derives identity from a
folder. Exactly one selector must match. Workspace and state roots must be
real, bounded directories; targets cannot escape them.

Materialization currently accepts only a verified local source boundary. The
source default branch must contain the exact candidate anchor before clone.
`active` uses a full clone; `reference` and `ephemeral` use blob filtering.
Publication is atomic: an incomplete temporary checkout is never exposed at
the target path.

Checkout removal never deletes immediately. A clean, exactly placed,
publication-proven checkout with no unsafe worktree state is atomically moved
to deterministic device quarantine. Restoration requires a separate explicit
plan.

## Portfolio planning

```text
gds portfolio plan --portfolio <id> --operation <kind> \
  --intent <single-line-intent> --inventory-root <path>
```

The planner accepts `repository-change`, `projection-rollout`, or
`policy-rollout`, discovers at most 2000 anchored boundaries with bounded
concurrency, builds a complete identity/relationship index, and selects exact
portfolio members. Each repository subplan records stable identity, path,
HEAD, manifest digest, policy digest, readiness, findings, and its own digest.
The aggregate plan records the ordered target-set digest and plan digest.

A blocked repository yields partial completion status while preserving every
independent ready subplan. The command does not mutate, publish, or claim an
unspecified repository patch.

## Deliberate provider gates

The following remain unavailable rather than approximated:

- GitHub repository transfer through the installation-token provider;
- network checkout materialization or network fork synchronization;
- GitHub push handlers, checks, reviews, merge, and Release;
- package registry release or package-consumer manifest update;
- force fork synchronization.

Rename, archive, deletion, and draft-PR provider primitives are locally
fixture-proven, but no live GitHub write is enabled or claimed.

Their future enablement requires a separately accepted provider, exact
permission and approval scope, isolated canary evidence, and rollback or
compensation behavior.
