# GDS repository projection v1 contract

Status: deterministic candidate generation, drift validation, and bounded
plan/apply/verify materialization implemented.

## Managed candidate files

With `agent.generated_agents: true`, the generator renders these files in
memory:

```text
.claude/CLAUDE.md
.gds/bundle.lock.yaml
.gds/compiled-policy.json
AGENTS.md
```

`AGENTS.md` is the standalone concise repository contract consumed natively by
nine target harnesses. `.claude/CLAUDE.md` is a first-class Claude adaptation
generated from the same typed inputs. It is not an `@AGENTS.md` import and is
not a second manual source. The Antigravity CLI adapter consumes the generated
`AGENTS.md` directly.

The generator consumes only typed repository fields, effective policy values,
verified repository commands, public-safe templates, and explicit bundle
metadata. It does not copy parent instructions, Serena memories, chat content,
or arbitrary repository documents.

With `agent.generated_agents: false`, only `.gds/compiled-policy.json` and
`.gds/bundle.lock.yaml` are managed. Existing `AGENTS.md` and
`.claude/CLAUDE.md` remain repository-owned first-class sources and are neither
rewritten nor classified as projection drift. This mode is the safe migration
boundary for repositories whose specific instruction contracts have not yet
been compiled into typed GDS inputs. Changing the flag is an explicit anchor
and projection migration, never an implicit generator decision.

## Digests

ADR 0015 defines three non-recursive layers:

- Markdown header `output-digest`: rendered body digest;
- lock `projection.files[].digest`: complete generated file digest;
- lock `projection.output_digest`: digest of the ordered path/file-digest list.

The input digest covers the repository anchor, compiled policy digest, bundle
metadata, and template digests. Tracked output has no timestamp. The bundle
lock is not allowed to list itself.

The target repository anchor must be committed before generation. Its commit
proves repository-owned input state but never becomes the reusable bundle
source. Development bundle `source_commit` is resolved only from the trusted
estate root's committed policy, schema, template, generator, and provider
sources. This prevents a managed repository from claiming that its own anchor
commit authored the shared GDS bundle.

Source identity is stable across hosted synthetic merge refs. When a merge
commit preserves one parent's exact canonical source boundary, that merge is
transparent and resolution continues through the equivalent parent. A merge
that materially changes any canonical source path remains the source commit.
This makes pull-request merge testing agree with the reviewed head without
ignoring conflict resolutions or other merge-authored source changes.

The generated Go CI caller therefore pins a reusable workflow that exposes a
typed `fetch_depth` input and passes `0` to both provenance-aware verification
jobs. Ref selection remains event-native, so GitHub still checks the synthetic
pull-request merge result. A positive fixed depth is not an accepted substitute:
the number of source-equivalent commits between the merge and the lock author is
unbounded.

Development locks use channel `development` and sequence `0`. Canary, stable,
or frozen locks require a positive sequence and attestation identity digest.
The development lock is test evidence, not a released immutable bundle.

Standalone public modules consume released policy without copying its source
tree into every repository:

```bash
gds generate repository \
  --bundle-archive /absolute/gds-bundle-vX.Y.Z.tar.gz \
  --release-envelope /absolute/release-envelope.json \
  --check
```

Both inputs are required together. GDS verifies the complete archive against
the detached envelope and embedded schemas, materializes only policy, schema,
template and public exception inputs in an owned temporary directory, and
requires the executing binary's embedded templates to match the release. The
resulting lock records release version, sequence, channel, artifact digest,
content-set digest and attestation identity. Plan/apply/verify require the same
two immutable input paths so precondition re-observation cannot change source.
Private targets cannot use this standalone boundary.

## Manual drift

Verification uses `lstat`, rejects symlinks and non-regular files, confines
paths to the exact target root, and compares complete file digests. It reports:

- `GDS_PROJECTION_MISSING`;
- `GDS_PROJECTION_MANUALLY_MODIFIED`;
- `GDS_PROJECTION_TYPE_INVALID`;
- path/read/security failures.

It never overwrites drift or attempts repair.

## Privacy

The compiler and generator enforce policy distribution against repository
visibility. Public projection tests also provide private relationship markers
to the typed input and prove those markers are absent from every output.
Repository commands are rendered with dynamically sized Markdown code spans so
backticks or Markdown-looking text cannot escape the command boundary.

## Read-only commands

```bash
gds generate repository --json
gds generate repository --check --json
gds validate projections --json
```

The first command returns candidate paths and digests without file contents or
writes. `--check` compares the candidate with current files. `validate
projections` is the same deterministic drift gate under the validator command
surface.

## Materialization

```bash
gds generate repository --plan --device-id <id> --session-id <id>
gds generate repository --apply <plan-id> --device-id <id> --session-id <id> \
  --approval-ref <reference>
gds generate repository --verify <operation-id> --device-id <id> --session-id <id>
```

The operation engine binds the exact candidate, current HEAD, manifest, policy,
and output fingerprint. Apply is limited to the exact candidate paths and uses
an opened `os.Root` capability rather than absolute path re-resolution. Every
parent is a real directory whose opened identity matches `lstat`; target reads
must remain the same regular file; existing content is capped at 8 MiB. Writes
use an unpredictable root-relative temporary file, file and directory `fsync`,
and root-relative atomic rename. Immediately before rename, materialization
reopens and compares the parent identity, then compares the target's captured
presence, regular-file identity, mode, and content digest; completed external
changes fail closed instead of being overwritten. Target or parent symlink
swaps cannot redirect the write outside the repository. A bounded in-memory
backup restores an earlier file only while the target still has the exact
identity, mode, and digest written by GDS. Newer external state is preserved and
the incomplete rollback is reported as an explicit partial conflict rather
than hidden.

Portable `os.Root` does not expose an atomic compare-and-replace or conditional
unlink primitive. The expected-state comparison is adjacent to rename or
remove, which narrows but cannot eliminate the residual operating-system
check/commit race. GDS does not claim stronger cross-process atomicity.

There is no generic arbitrary-file materializer. Manual drift remains a
validation failure and is never overwritten without the exact stored plan and
approval.
