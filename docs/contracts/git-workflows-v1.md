# GDS owner Git workflows v1 contract

Status: session start, checkout synchronization, unfinished-work handoff, and
complete-work are implemented against verified local providers. Live GitHub
publication, pull-request integration, and provider check evidence remain
disabled until their provider gates are accepted.

## Git subprocess authority

`core/gitauthority` owns every production Git subprocess used by provider,
release-builder, and runtime-fixture code. Discovery ignores caller `PATH` and
selects one reviewed darwin/Linux system location. It resolves symlinks once,
requires the executable and every discovery ancestor to be system-owned and
non-writable by group/other, captures its file identity and SHA-256 digest, and
rechecks identity before and after every command. User-owned paths exist only
as explicit test/dependency-injection authorities. Read and mutation runners
share the process-wide authority instead of resolving Git separately.

Git receives a constructed allowlist environment, not `os.Environ`. System and
global configuration are disabled; repository/worktree/common-dir/index,
object alternates, namespaces, replacement refs, executable Git/SSH helpers,
external diff/editor hooks, and dynamic-loader injection variables cannot be
inherited from the caller. Network certificate/proxy values and `SSH_AUTH_SOCK`
are the only inherited transport inputs. Command-specific author/committer
identity and the isolated release HOME are accepted only through explicit
allowlisted overrides. Repository-local configuration remains observable, and
the existing per-workflow validators continue to reject command-bearing local
configuration before network or worktree mutation.

## Session start

```text
gds session start --scope current --refresh none|origin
```

The command resolves the current GDS context, current/superproject boundaries,
and initialized recursive submodule boundaries with a hard limit of 64. Every
boundary receives a machine-readable Git classification, safe actions, and
blocked actions.

`--refresh none` is fully read-only and labels remote evidence `unknown` and
classifications `*-cached`. It never claims cached refs are current.

`--refresh origin` is an explicit non-integrating ref refresh. It:

- validates one exact origin URL and blocks HTTPS userinfo, SSH users other
  than credential-free `git`, passwords, remote helpers,
  executable Git configuration, includes, filters, proxies, and unsafe
  protocols;
- uses the canonical allowlisted Git environment, which contains no
  command-bearing Git/SSH variables;
- disables system/global Git config for every Git process;
- uses fixed argv, no tags, no submodules, no auto-GC, no `FETCH_HEAD`, and an
  explicit remote-tracking refspec;
- snapshots refs before and after and classifies create, fast-forward, delete,
  and forced-update transitions by object ancestry;
- requires a current private GDS state database before network mutation and
  stores the exact post-fetch refs, local HEAD, observation time, digest, and
  forced-update flag for later plan preconditions;
- never checks out, merges, rebases, fast-forwards the current branch, pushes,
  clones, prunes, stages, commits, or cleans.

The envelope truthfully reports local ref mutation when refresh was attempted.
A forced update is a conflict and blocks synchronization. Dirty, diverged,
detached, conflicted, unborn, and no-upstream states remain unchanged.

## Checkout synchronization

```text
gds sync checkouts --plan [--checkout <path> ...]
gds sync checkouts --apply <plan-id> --approval-ref <ref>
gds sync checkouts --verify <operation-id>
```

Planning defaults to the current checkout and accepts only explicitly selected
additional roots. A checkout is eligible only when it has:

- a stable GDS repository identity and valid compiled policy;
- an attached clean branch with an `origin/*` upstream;
- zero local-ahead commits, at least one behind commit, and no divergence;
- no staged, unstaged, untracked, conflicted, or modified-submodule state;
- fresh durable session-refresh evidence whose HEAD and full sorted origin-ref
  digest still match the checkout;
- no forced-update evidence.

Dirty, diverged, detached, conflicted, unborn, no-upstream, current, stale,
force-updated, and otherwise unproved boundaries are reported and preserved;
they are not silently added to a partial mutation plan.

Apply uses the C3 operation engine, exact approval evidence, repository locks,
plan expiry, and precondition rechecks. The only worktree-changing command is a
fixed-argv `git merge --ff-only` to the exact planned OID. It runs with system
and global Git config disabled, a safe executable path, command-bearing Git
environment removed, executable local config rejected, hooks redirected to an
isolated empty directory, and submodule recursion disabled. It never fetches,
rebases, creates a merge commit, pushes, prunes, stages user files, or cleans.

Verification independently proves the exact branch, target HEAD, upstream OID,
and clean checkout state from journaled after-evidence.

## Unfinished-work handoff

```text
gds handoff --plan --file <path> ... --message <message> \
  --author-name <name> --author-email <email>
gds handoff --apply <plan-id> --approval-ref <ref>
gds handoff --verify <operation-id>
```

Planning requires an explicit sorted file set. Each selected path must be a
changed bounded regular file or an explicit tracked deletion; symlinks,
directories, unchanged paths, `.git`, traversal, duplicates, and implicit
untracked discovery are rejected. The plan records content and porcelain-status
digests, exact branch/HEAD/remote OIDs, manifest and policy digests, commit
identity/message/time, required check commands, and draft-PR policy.

Required checkpoint checks are currently emitted honestly as `NOT_PROVEN` in
the approval scope; they are never reported green without execution evidence.
A future deterministic verification-evidence runner can strengthen that field
without changing commit/push safety. `handoff_pr: required` blocks planning
until the C8 provider can create or update the draft PR. `preferred` remains a
visible not-proven notice.

Apply is prohibited on the protected default branch and on conflicted,
force-updated, stale, diverged-remote, or unproved state. The C4 provider only
permits a real non-symlink local bare remote; HTTPS/SSH push is rejected before
the operation engine or commit handler starts. This makes commit and push fully
testable without enabling live GitHub writes early.

The handler disables hooks and signing, rejects command-bearing Git config,
uses fixed argv, commits only the approved paths, proves the new commit has one
exact parent and exactly the approved changed-file set, then performs an exact
lease-bound fast-forward push. Unrelated staged, unstaged, untracked, branch,
and worktree state is preserved. It never merges, marks a PR ready, or cleans.

## Completed-work integration

```text
gds complete --plan --task-id <task-id> [--checkout <path> ...]
gds complete --apply <plan-id> --approval-ref <ref>
gds complete --verify <operation-id>
```

Planning accepts an explicit bounded checkout set and a canonical task ID. Each
checkout must prove all of the following:

- an attached, clean, non-default task branch with its exact `origin/*`
  upstream;
- the task OID is already published and is a strict fast-forward descendant of
  the exact current local and remote default OID;
- fresh durable origin-ref evidence matches the complete sorted ref set and
  current HEAD, with no forced update;
- a valid repository anchor and compiled policy;
- no configured required check lacking execution evidence;
- no default or task branch active in another worktree.

The planner builds a typed dependency graph. A selected `git-submodule-consumer`
must pin the selected module's exact final task OID at a clean or uninitialized
gitlink. Module steps precede consumer steps. Package relationships block until
a verified release contract can produce a final package version. Cycles,
missing final pins, and unresolved topology block the whole plan.

The operation engine rechecks every selected Git boundary globally and again
immediately before that boundary's first mutation step. Drift before any step
produces a stale plan with no mutation. Drift after an earlier boundary
succeeds produces a durable partial saga and does not call later handlers.

Every branch operand uses one shared bounded `git check-ref-format --branch`
subset. Option-like leading dashes, leading-dot components, `.lock` suffixes,
empty components, traversal-like separators, and Git metacharacters are
rejected before subprocess construction. The reserved shorthand `HEAD` is not
a branch name and is rejected exactly; lowercase `head` remains valid. Commands
that accept a branch also receive an explicit `--` operand boundary.

Apply currently accepts only a real non-symlink local bare remote. It uses
fixed argv, command-environment stripping, disabled hooks/signing/submodule
recursion, exact leases, and strict fast-forward ancestry. For each repository
it:

1. advances the remote default ref from the exact expected OID to the published
   task OID;
2. verifies the remote default;
3. advances the local tracking/default branch and switches the checkout;
4. deletes the remote-tracking and local task refs only after reachability is
   proven;
5. verifies a clean default checkout and absence of both task refs.

HTTPS, SSH, pull-request integration, and live check evidence are blocked
before the operation engine starts. Completion never force-rewrites history,
never removes an active worktree, and never integrates a consumer before its
selected module finalization. Real two-repository fixtures prove that consumer
main ends on the final module gitlink and that both default refs are published
before task cleanup.

## Provider boundary

The following work is intentionally outside the local C4 provider:

```text
live GitHub push
draft pull-request creation or update
required check/review observation
pull-request merge
```

Those actions require the separately permissioned GitHub provider and remain
unavailable rather than being approximated or reported as proven.
