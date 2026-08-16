# Phase 07 Git, module, and fork evidence

Status: read-only foundation complete; mutation workflows remain disabled.

Date: 2026-07-11

## Completed

- Added bounded Git topology commands for remotes, `.gitmodules`, gitlinks,
  initialized submodule HEADs, and cached remote-tracking comparisons.
- Added credential/query redaction for fetch, push, and submodule URLs.
- Added typed module relationship validation and fork lifecycle inspection.
- Fixed typed repository-anchor data loss for module, fork, and provider alias
  sections and made post-schema domain decoding reject unknown fields.
- Split branch-name and repository-name schema contracts so safe branch names
  such as `release/1.0` remain representable.
- Added `gds git topology`, `gds module inspect`, `gds validate gitlinks`, and
  `gds fork inspect`.

## Evidence

- real temporary Git repositories cover gitlinks, at-pin/off-pin worktrees,
  cached divergence, detached forks, and multiple remote identities;
- traversal, duplicate submodule paths, credential-bearing URLs, unsafe command
  shapes, and cross-scope refs are rejected;
- read-only topology inspection preserves the Git index byte-for-byte;
- race-enabled tests cover Git, module, and fork packages;
- no refresh, integration, force update, push, or cleanup command is allowed.

## Not proven

- remote ref freshness;
- module target identity without the compiled estate identity index;
- published-commit and release eligibility;
- actual sync/module/fork plan/apply handlers;
- recovery after a real Git mutation.

## Next dependency

Live provider observation and a separately approved mutation rollout are needed
before any local network refresh or integration handler can be enabled.
