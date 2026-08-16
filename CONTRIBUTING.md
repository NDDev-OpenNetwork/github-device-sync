# Contributing

Thanks for considering a contribution to GDS. This repository is a control
plane: it compiles typed canonical inputs into immutable policy bundles and
repository-local projections. That shape drives most of the rules below.

Please read [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) first, and report security
problems privately per [SECURITY.md](SECURITY.md) rather than in a pull request.

## The one rule that surprises people

**Never hand-edit a generated projection.** These files are compiled output:

- `AGENTS.md` and `.claude/CLAUDE.md`
- `.serena/memories/**`
- `.gds/bundle.lock.yaml` and `.gds/compiled-policy.json`

Change the declared canonical input instead — `.gds/repository.yaml`,
`policies/**`, `schemas/**`, `templates/**`, or `estate/**` — and regenerate:

```sh
# Repository projections (AGENTS.md, .claude/CLAUDE.md, ...)
gds generate repository --help

# Provenance-bearing Serena memories
gds memory generate --help
gds memory validate --json
```

A pull request that edits a projection by hand and leaves the canonical input
untouched will be rejected, because the next regeneration silently reverts it.

Memory regeneration is commit-first: a memory records the `source_commit` its
content was derived from, and `verified_at` must not precede that commit. If you
change a file listed in a memory's `sources:`, commit that change, regenerate,
and only then promote the memory.

## Getting set up

Go is the only required toolchain for the core build; the Python test tiers
bootstrap their own pinned `uv` environment.

```sh
go build -trimpath -o /tmp/gds ./core/cmd/gds
/tmp/gds context --json
```

`gds` is not installed on your `PATH` by this repository — build it to a
scratch path as above and invoke it explicitly while developing.

## Verification tiers

Run the tier that matches the size of your change. Each tier is a superset of
the cheaper one.

| Tier | Command | What it runs |
| --- | --- | --- |
| Fast | `scripts/validate_ci_tier.sh fast` | `validate_go_core.sh --quick` |
| PR-required | `scripts/validate_ci_tier.sh pr-required` | full Go core validation, `pytest`, assurance (tests skipped), sync tests |
| Full | `scripts/validate_ci_tier.sh full` | as above with the complete assurance suite |
| Release | `scripts/validate_ci_tier.sh release` | `scripts/validate_release.sh` |

**Pull requests must pass at least `pr-required`.** Also run
`gds validate --json` and `gds doctor --json`; both are expected to exit `0`
with zero findings, so a non-zero exit is a real signal, not background noise.

Two practical notes:

- `go test ./core/...` catches contract and golden-file breakage that
  `gds validate` alone does not. Run it when you touch `core/**`.
- Several tests assert exact remote URLs. A global `url.*.insteadOf` rewrite in
  your `~/.gitconfig` will fail them and can trip GDS remote-security checks.
  If you use one, run the tiers with `GIT_CONFIG_GLOBAL=/dev/null`.

The race-heavy assurance and controller suites are contention-sensitive. Run the
full tier on an otherwise idle machine, and confirm any suspected flake in
isolation before reporting it.

## Commits and pull requests

- **Conventional Commits.** Subject lines follow `type(scope): summary` — for
  example `fix(memory): reject verified_at preceding source commit` or
  `chore(projections): regenerate for public classification`. Keep the subject
  under 100 characters.
- **Task branches, pull-request integration.** Branch from `main`, open a pull
  request, and let merged branches be cleaned up. Do not push to `main`.
- **One mutation boundary per pull request.** This repository vendors
  independent Git repositories under `modules/**`. Changes inside a submodule
  belong in that submodule's own repository and pull request; a control-plane
  pull request should only move the gitlink, and should say why.
- **Regenerate last.** When a change touches a bundle source path, the final
  commit on the branch must be the projection and memory regeneration, so the
  recorded source commit matches what actually shipped. Merge such branches
  rather than squashing, which preserves that provenance.
- Add a `CHANGELOG.md` entry under `## Unreleased`.

Commit signing is not required in this repository.

## What makes a change easy to accept

- It changes canonical input, not compiled output.
- It keeps the public/private boundary intact: no private estate context, token,
  or provider observation is persisted into a public repository.
- It leaves unrelated dirty state, branches, worktrees, and submodules alone.
- It reports verification honestly. If a required check could not be run, say so
  and mark it `NOT_PROVEN` rather than implying it passed.
