# GDS harness adapter v1 contract

Status: deterministic projection and transactional adapter lifecycle are
implemented. Typed evaluation evidence runs locally; model-dependent runtime
cases remain independently `NOT_PROVEN` until exact transcripts pass.

## Canonical identities

The catalogue is every harness GDS can render. It is owned by
`harnesses/capability-registry.yaml` and mirrored by
`core/harness.CanonicalIDs`; `scripts/validate_harness_docs.py` fails when this
list, the registry, and that constant disagree, so the three cannot drift apart
silently as they did before.

```text
antigravity-cli
claude-code
cline
codex
cursor-cli
github-copilot-cli
grok-build
junie-cli
kilo-cli
kimicode
kiro-cli
mimocode
opencode
pi
qoder-cli
qwen-code
zcode
```

A catalogue entry is not a promise that the harness is installable yet. An entry
whose profile declares `skill_strategy: "not-proven"` is a valid, honest
provisional registration: `gds validate harnesses` passes on it, and
`gds harness render` still refuses it with `GDS_HARNESS_PROJECT_SKILLS_NOT_PROVEN`.
Validity and renderability are different questions.

## One-source projection model

- Repository facts live in `.gds/repository.yaml`.
- Effective reusable rules live in the pinned GDS bundle.
- `AGENTS.md` is the standalone generated projection for every target with
  native AGENTS support.
- `.claude/CLAUDE.md` is a first-class Claude adaptation generated from the
  same typed inputs. It never imports or manually copies `AGENTS.md`.
- Canonical skill procedures live once in `skills/canonical`.
- Explicit-only canonical skills use `disable-model-invocation` for Claude
  Code, Pi, and Kimi Code; Codex uses generated `agents/openai.yaml`; ZCode uses
  manual `$skill`. Harnesses without a proven native control receive no
  destructive explicit-only skills (`profile-exclusion`).
- Other harness-specific paths receive only generated copies, packages, or
  verified local symlinks. No manual fork of skill content is allowed.

## Documented capability matrix

| Harness | Instruction projection | Skill discovery | Explicit-only control | Runtime status |
|---|---|---|---|---|
| Antigravity CLI | workspace-root `AGENTS.md` | `.agents/skills` | profile exclusion | supported, delegated evidence |
| Claude Code | `.claude/CLAUDE.md` | project/plugin skills | `disable-model-invocation` | supported, delegated evidence |
| Codex | root-to-CWD `AGENTS.md` | `.agents/skills` and plugins | `agents/openai.yaml` | supported, delegated evidence |
| Cursor CLI | workspace-root `AGENTS.md` | `.cursor/skills` | profile exclusion | supported, delegated evidence |
| Grok CLI | root-to-CWD `AGENTS.md` | `.grok/skills`, user `.agents/skills` | profile exclusion | supported, delegated evidence |
| Kimi Code | native project `AGENTS.md` (order runtime-gated) | `.agents/skills`, `.kimi-code/skills` | `disable-model-invocation` | provisional |
| MiMo Code | workspace-root `AGENTS.md` | `.mimocode/skills`, `.agents/skills`, `.claude/skills` | profile exclusion | provisional |
| OpenCode | root-to-CWD `AGENTS.md` | `.agents/skills`, `.opencode/skills`, `.claude/skills` | profile exclusion | supported, delegated evidence |
| Pi | parent-chain `AGENTS.md` | `.agents/skills`, `.pi/skills` | `disable-model-invocation` | supported, delegated evidence |
| ZCode | workspace-root `AGENTS.md` | `.zcode/skills`, managed user skills | manual `$skill` | provisional |

The seven supported rows use a declared delegated evidence owner. Stable and
frozen releases still require fresh signed evidence for the exact active-seven
closure; catalogue-only rows remain provisional and on-pause.

## Validation

```bash
gds validate harnesses --harness all --json
gds validate harnesses --harness <canonical-id> --json
```

Validation enforces:

- exact canonical set;
- unique IDs and aliases;
- profile path derived from ID;
- registry/profile status, date, runtime, and alias parity;
- HTTPS source authority;
- coherent instruction and skill projection declarations;
- `supported` forbidden until the required runtime result is `pass`;
- Codex-specific marketplace, package, sidecar, and hook contracts.

## Catalogue versus device selection

GDS keeps two separate facts, and conflating them is the most common way to get
this wrong.

- **The catalogue** is every harness GDS can render — the list above. It grows
  whenever a harness is registered and says nothing about any device.
- **The device selection** is `harnesses:` in `estate/devices/<device>.yaml`: the
  subset one device actually runs, normally two or three.

Static contracts apply to the whole catalogue, but runtime evidence is required
only for the selected set (`harness.ValidateSelected`). A provisional catalogue
entry therefore never blocks a device that does not run it. Do **not** add every
catalogue entry to a device to "register" it: the catalogue is the registration,
and the device list is a choice.

```bash
gds harness sync --device <descriptor> --target-root <root> --json
```

`gds harness sync` reconciles the two. It is read-only, and for one device it
classifies every catalogue entry:

| action | condition |
|---|---|
| `install` | selected, absent |
| `update` | selected, present, drifted |
| `remove` | installed, no longer selected |
| `current` | selected and matching |

An entry that is neither selected nor installed is omitted: it is not that
device's concern. Any pending action reports `GDS_HARNESS_SELECTION_DRIFT`.

Sync refuses ambiguous and unprovable input rather than guessing:

- `GDS_HARNESS_SELECTED_UNKNOWN` — a selected id the catalogue does not know.
  Nothing is scheduled; GDS cannot render a profile it does not have.
- `GDS_HARNESS_SELECTION_EMPTY` — an empty `harnesses:`. A blank field reads the
  same as "not filled in yet", and classifying it would propose removing every
  installed projection.
- `GDS_HARNESS_UNOBSERVABLE_NOT_PROVEN` — unselected entries whose adapter could
  not be built or inspected, so a leftover install of one would not be reported.
  A selected harness in that state is a hard error instead: the device cannot
  converge on it.

The removed `--converge` flag now fails closed. Apply one reported action:

```bash
gds harness <install|update|remove> --harness <id> --target-root <root> --plan
gds operation approve <plan-id> --private-key <key> --output <approval.json> ...
scripts/gds-exact-apply.sh --plan-id <plan-id> --approval-file <approval.json> \
  --state-path <db> --device-id <id> --session-id <id> -- \
  gds --json harness <install|update|remove> --harness <id> --target-root <root>
```

Convergence is a **sequence** of the existing single-harness transactions, not one
atomic plan, and the reason is structural: the operations engine resolves an
action handler by action name, while every install shares
`materialize-harness-adapter` and each handler is bound to one exact target and
candidate. A multi-step plan would therefore apply the first harness's projection
for every later step of the same action. Sequencing keeps each mutation inside a
transaction that is already proven and approval-gated.

Each entry runs the full plan -> apply -> **verify** shape, not just plan and
apply: a sequenced run that skipped verification would report success on an apply
whose result was never confirmed, and the next harness would then build on an
unverified target. A step is reported `verified` only after that third call
succeeds.

Combined convergence is removed. A single command cannot create multiple plans
and honestly bind one signed approval to every exact digest. Each reported
action is therefore a separate plan → signed approval → enable → apply → verify
transaction. Re-running read-only sync after every transaction produces the
remaining work without hiding partial state.

`applied` and `verified` are separate counts because the two can differ by
exactly one entry. A target has mutated the moment its apply succeeds, so an
entry whose *verification* then fails is reported `applied-unverified`: counted
in `applied`, absent from `verified`, and not listed as remaining work. Counting
it only after verification would describe an already-changed target as untouched
and still queued, which is the one thing a recovering operator must not be told.

Drift is the reason to converge, so `GDS_HARNESS_SELECTION_DRIFT` does not block
it. Neither does `GDS_HARNESS_UNOBSERVABLE_NOT_PROVEN`: a device declares which
harnesses it runs, and GDS does not gate that selection on its ability to
introspect the ones the device did not pick — verifying harnesses themselves
belongs to the harness repository, not this estate. It is not discarded either.
The finding travels into the convergence result, which then reports
`not-proven` (exit 3) instead of success: every selected entry converged, but
whether a leftover projection of an unread entry still sits at the target was
not established. Every other finding blocks, including
`GDS_HARNESS_TARGET_COLLISION`.

## One target root holds only non-overlapping harnesses

`PlanInstall` refuses to overwrite any managed path it does not already own. Two
harnesses whose skill roots overlap therefore cannot occupy one target root:
`codex` and `opencode` both project into `.agents/skills/`, the AGENTS standard,
while `zcode` uses `.zcode/skills/` and coexists with either.

Read-only `sync` reports this as `GDS_HARNESS_TARGET_COLLISION` before any
mutation. No combined mutation mode exists.

The comparison is between *desired* candidates, not against what the target
currently holds. On an empty root nothing is installed yet, so a check that only
asked "is this path already taken" would see no conflict, install the first
harness, and let the second discover the collision after the mutation the
read-only pass promised to prevent. Two selected harnesses claiming one path are
therefore reported before either is installed. Resolving it is an owner decision —
select one of the overlapping harnesses for that root, or give them separate
target roots — and GDS does not choose silently.

Sync names which existing single-harness transaction converges each entry and is
strictly read-only. The operator executes removals before installs as separate
exact-plan transactions, so replacement cannot silently create overlapping
ownership.

## Runtime detection

```bash
gds harness detect --harness all --json
```

Detection performs only bounded `--version` commands declared by profiles. It
does not install, update, authenticate, configure, trust hooks, or write state.
Binary presence proves only the observed executable and version string.

## Runtime support gate

```bash
gds harness eval \
  --harness <canonical-id> \
  --skill-profile core \
  --model-label <exact-label> \
  --execution-profile read-only \
  --tool <available-tool> \
  --json
```

The evaluation envelope records the exact executable/version when available,
OS, architecture, capability-profile digest, model label, execution profile,
tools, all twelve cases, six quality metrics, transcripts, aggregate status,
and a canonical result digest. Deterministic adapter cases may pass without a
model. Discovery, invocation, trigger, output, hook, and context-firewall cases
remain `NOT_PROVEN` until runtime transcripts and assertions are supplied.

Native execution uses one explicit driver protocol:

```bash
gds harness eval \
  --harness <canonical-id> \
  --skill-profile <profile> \
  --model-label <exact-label> \
  --execution-profile read-only \
  --tool <available-tool> \
  --runtime-driver /absolute/path/to/trusted-driver \
  --evidence-directory /absolute/path/to/empty-evidence-directory \
  --driver-timeout 30m \
  --json
```

The executable is invoked directly, never through a shell. GDS passes a typed
request on stdin, filters inherited environment variables, bounds time/stdout/
stderr, requires an empty non-symlink evidence directory, and preserves the
exact request. Driver stdout must satisfy
`schemas/v1/harness-runtime-evidence.schema.json`.
The request also binds the exact active `gds` executable used by plugin hooks;
the driver must not resolve an ambient or different-version controller binary.

The released Codex implementation is `gds-codex-runtime-driver`. It uses two
bounded workers, exact model selection, isolated homes and Git fixtures,
schema-bound subject/judge outputs, and transcript checkpoints. If the outer
evaluation is interrupted, rerun the driver directly with the preserved
`driver-request.json` on stdin and the same evidence directory, then validate
the resulting JSON through `--runtime-evidence`; existing checkpoints are
accepted only when every request/task/prompt identity still matches.

Already captured evidence can be revalidated without executing a harness:

```bash
gds harness eval \
  --harness <canonical-id> \
  --skill-profile <profile> \
  --model-label <exact-label> \
  --execution-profile read-only \
  --tool <available-tool> \
  --runtime-evidence /absolute/path/to/runtime-evidence.json \
  --json
```

Acceptance binds the current executable/version, OS/architecture, exact model,
tools, capability profile digest, runtime contract, all seven native cases,
all six metrics, and every canonical corpus sample/run. Semantic output
assertions additionally bind one exact supported judge harness, version, model,
execution profile, tools, and rubric-prompt digest; a subject model cannot
self-certify its own output. Transcript references must be unique confined
regular files whose byte count and SHA-256 match.
One transcript is one metric attempt and cannot be duplicated as another
attempt. Multiple runtime cases may bind the same transcript digest when one
native observation proves several case contracts, such as exact skill discovery
from both root and nested discovery probes.

Implicit positive-recall attempts are derived only from skills whose canonical
registry invocation mode is `implicit`. Explicit-only skills are covered by
exact explicit invocation; their natural positive intents are specificity
samples that must not activate the skill implicitly. Those explicit-only
samples additionally have a zero-tolerance destructive case gate, independent
of the general specificity threshold. When a profile contains no implicit
skills, positive recall is `not-applicable` with zero attempts; it must never be
manufactured by enabling implicit access to a destructive skill.

`gds harness sync` is read-only reconciliation. Mutations use the existing
single-harness `install`, `update`, and `remove` transactions through the common
durable operation engine:

```text
gds harness <action> --harness <id> --target-root <root> \
  --state-path <db> --device-id <id> --session-id <id> \
  [--plan|--apply <plan-id>|--verify <operation-id>]
```

Each transaction retains the per-harness plan/approve/enable/apply/verify shape.
`rollback` is not emitted by convergence; it remains available as a separate
single-harness recovery verb:

```text
gds harness rollback --rollback-source <exact-prior-root> --plan|--apply|--verify
```

Update binds the previous and desired file sets, rejects unmanaged collisions,
removes only exact-digest stale managed files, and compensates safely on
failure. Rollback accepts only a prior standalone projection whose lock and
managed contents verify exactly.

Promotion to `supported` requires an isolated fixture for the exact version:

1. install into a clean harness home;
2. start from repository root and a nested directory;
3. inspect loaded instructions and exact discovered skills;
4. explicitly invoke a read-only skill;
5. prove a destructive skill does not invoke implicitly;
6. exercise applicable hooks and permission controls;
7. test public/private context isolation;
8. test update, rollback, and removal;
9. record harness version, model label, execution profile, tools, results, and
   evidence digest.

Static files, official docs, or a successful version command never satisfy
this gate.
