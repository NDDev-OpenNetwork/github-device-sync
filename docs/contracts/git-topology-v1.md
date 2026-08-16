# GDS local Git topology v1 contract

Status: read-only inspection, explicit non-integrating refresh, and isolated
local mutation providers are implemented. Live network publication and GitHub
integration remain disabled.

## Commands

```text
gds git topology
gds module inspect
gds validate gitlinks
gds fork inspect
```

The adapter uses bounded machine-facing Git commands and sets
`GIT_OPTIONAL_LOCKS=0`. It reads repository/worktree identity, remotes,
`.gitmodules`, index gitlinks, submodule HEADs, worktrees, status, and cached
remote-tracking refs. It never fetches, checks out, integrates, pushes, cleans,
or changes Git config.

## Remote safety

- fetch and push URLs are inspected independently;
- credentials and query material are redacted before result serialization;
- credential-bearing URLs are critical findings;
- origin and upstream identities are compared with typed manifest facts;
- redirects, remote helpers, and network access are not invoked;
- cached refs are always labelled `cached-unknown`, never current.

## Local recovery-ref primitive

The only C3 Git mutation handler can compare-and-swap one
`refs/gds/recovery/*` reference to an existing exact object ID. It requires an
exact expected old OID (all-zero only for creation), uses fixed argv and reflog
text, rejects every branch/tag/remote namespace, never changes HEAD/index/
worktree, never accesses the network, and verifies the final ref. It is not a
standalone CLI escape hatch; a later domain plan must register it through the
operation engine with approval and precondition evidence.

## Gitlink contract

- `.gitmodules` is parsed by Git with includes disabled and an exact key
  allowlist;
- submodule paths must be normalized repository-relative paths;
- path duplication and traversal are rejected;
- mode `160000` index entries are read from NUL-delimited stage output;
- configuration, gitlink, and typed relationship names must agree;
- initialized submodule HEAD is classified as at-gitlink or off-gitlink;
- a symlink/non-directory at the submodule path is unsafe;
- target GDS identity remains not proven until the estate identity index exists.

## Fork contract

`gds fork inspect` validates manifest metadata and origin/upstream identities,
then compares cached tracking refs. `gds fork sync` is a separately approved,
fast-forward-only local-provider workflow; it never force-syncs or discards
fork-only commits. Unexpected fork-only commits under `upstream-tracking`
block handling. Detached forks do not require a live upstream remote. Cached
inspection remains `NOT_PROVEN` until explicit freshness evidence exists.
