# GDS Codex plugin package v1 contract

Status: deterministic static package candidates; no plugin is installed or
trusted by this contract.

## Canonical source and package boundary

The control plane stores plugin-specific source only under:

```text
plugins/<plugin>/.codex-plugin/plugin.json
plugins/gds-core/hooks/
```

Canonical skills remain under `skills/canonical/`. `gds skill package
<plugin>` builds a standalone candidate in memory by selecting registered
profiles and copying only `SKILL.md` and `agents/openai.yaml` into the package.
Source plugin directories must not contain hand-maintained skill copies.

Each candidate contains `gds-package.json` with:

- plugin identity;
- canonical registry digest;
- ordered source-file paths, sizes, and digests;
- a package digest over the ordered non-recursive file manifest.

Generation is byte-identical for equal inputs. Package paths are relative,
bounded, and written through an `os.Root` only when the trusted release builder
explicitly materializes a new destination.

## Plugin split

- `gds-core` owns common repository/device skills and the only shared lifecycle
  hook source.
- `gds-estate-admin` owns control-plane and portfolio skills.
- `gds-module` owns module lifecycle skills.

The repository marketplace at `.agents/plugins/marketplace.json` marks all
three packages available, not installed by default.

## Hooks

`gds-core/hooks/hooks.json` uses the documented plugin default path and owns:

- `SessionStart`: inject compact `gds context --json` evidence;
- `PreToolUse`: deny a narrow set of obvious direct destructive Git/GitHub
  commands;
- `Stop`: report repository validation failure without an automatic
  continuation loop.

Hook commands resolve through `$PLUGIN_ROOT`. The adapter executes `gds` only
from an absolute, non-symlink `GDS_BIN` and passes a small environment allowlist.
Missing CLI evidence is reported as `NOT_PROVEN`.

Hooks are guardrails. They do not intercept every equivalent tool path, they
do not grant approval, and changed definitions require Codex trust review.
Critical enforcement remains in GDS CLI, sandbox policy, provider permissions,
and GitHub governance.

## Validation and runtime boundary

`gds validate plugins` validates and hashes all standalone candidates without
writing them. `gds validate harnesses --harness codex` additionally validates
the capability profile and marketplace, then returns exit 3 until isolated
runtime tests prove discovery, explicit-only behavior, hooks, and visibility.

Global installation, hook trust, `$CODEX_HOME` modification, release, and
rollout are outside this phase and require their applicable approval.
