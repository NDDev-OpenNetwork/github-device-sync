# GDS canonical skills v1 contract

Status: Phase 05 static contract. Runtime model evaluations remain
`NOT_PROVEN`.

## Authorities

- `skills/registry.yaml` owns the `gds-*` namespace, profile membership,
  invocation mode, mutation class, Codex interface metadata, and context
  budgets.
- `skills/canonical/<name>/SKILL.md` owns one portable procedure.
- `agents/openai.yaml` is a Codex projection and must equal registry metadata.
- `skills/evals/` owns non-secret evaluation inputs, never runtime results.
- Generated plugin copies are build artifacts and are not canonical sources.

## Profiles

- `core`: orientation, one-repository audit, handoff, completion, and local
  context maintenance.
- `estate-admin`: control-plane audit, planning, lifecycle, recovery,
  maintenance, release, and rollout.
- `module`: module relationship, release, and consumer-pin workflows.
- `device`: bootstrap, materialization, and checkout synchronization.
- `portfolio`: drift triage and bounded portfolio-wide change.

The three Codex packages select profiles without installing all estate skills
globally:

```text
gds-core          = core + device
gds-estate-admin  = estate-admin + portfolio
gds-module        = module
```

Duplicate skills within a plugin union are deduplicated by canonical name.
Codex must never merge same-name skills from separate active sources.

## Skill requirements

Every canonical skill:

- uses a lowercase `gds-*` name matching its directory;
- has `name` and `description` portable frontmatter;
- sets `disable-model-invocation: true` when the canonical registry marks it
  `explicit-only`; Claude Code, Pi, and Kimi Code consume this shared native
  field, while other adapters may ignore it;
- states positive and negative routing boundaries in the description;
- contains Contract, Use when, Do not use when, Inputs, Preconditions,
  Workflow, Stop conditions, Verification, Output, and References sections;
- stays below the internal 300-line and 600-description-character budgets;
- references no private or control-plane-only runtime file;
- delegates exact repeated mechanics to structured `gds` commands.

External mutations are always `explicit-only`. Their portable source sets:

```yaml
disable-model-invocation: true
```

Their Codex sidecars additionally set:

```yaml
policy:
  allow_implicit_invocation: false
```

This routing control is not authorization. Every future mutation remains
subject to deterministic plan, approval, precondition recheck, apply, verify,
and journal gates.

## Static validation

`gds validate skills` checks:

- strict registry schema;
- unique names, profiles, and plugin IDs;
- exact profile references and complete profile membership;
- canonical path containment and non-symlink sources;
- frontmatter, descriptions, sections, line budget, and TODO markers;
- registry-to-Codex-sidecar equality;
- explicit-only external mutations;
- portable explicit-only frontmatter parity;
- plugin metadata budgets;
- schema-valid trigger and output corpora for every canonical profile;
- the common critical-enforcement corpus;
- exact train/validation, query-count, repeat-count, profile-membership, and
  output-assertion coverage.

The current package metadata use is:

```text
gds-core           2588 / 8000 characters
gds-estate-admin   3893 / 8000 characters
gds-module         1009 / 8000 characters
```

## Behavioral evidence

Every file under `skills/evals/trigger/` contains eight positive and eight
near-miss negative queries per profiled skill, split into train and validation
sets and configured for three runs per query. Matching files under
`skills/evals/output/` declare hard assertions. The common enforcement corpus
covers mutation planning, stale state, remote rewrites, visibility, dependency
publication, cleanup reachability, and approval scope.

Stored cases are inputs, not results. Discovery, explicit invocation, trigger,
output, and enforcement claims require an isolated runtime record naming the
exact harness version, model label, execution profile, tools, date, and result
digest. GDS derives metric totals from one unique transcript per exact
sample/run and rejects incomplete or substituted coverage.
