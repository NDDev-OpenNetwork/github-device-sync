# GDS skill evaluations

This directory contains canonical, non-secret evaluation inputs. Static skill,
profile, sidecar, explicit-only, plugin-package, and hook contracts run locally.
Model-dependent discovery, trigger, and output results are separate evidence and
must record the exact harness, harness version, model label, execution profile,
tools, date, and result digest.

`trigger/` and `output/` contain one corpus for every canonical skill profile:
`core`, `device`, `estate-admin`, `module`, and `portfolio`. Every implicitly
routable skill has eight positive and eight near-miss negative queries split
between train and validation, with three required runs per query.
Positive-recall runtime metrics include only skills whose canonical invocation
mode is `implicit`. Explicit-only skills retain description-quality prompts but
their positive intents are evaluated as specificity samples: the skill must not
activate implicitly. Those samples also form the zero-tolerance destructive
implicit-invocation case. Profiles with no implicit skills report positive
recall as `not-applicable` with zero attempts.

Every output task has one stable task ID, one exact user-style prompt, and
typed assertions. Deterministic assertions bind to filesystem, Git, tool-event,
or exact skill-read evidence. Semantic assertions use an explicit rubric and
must be graded from the named final-output and tool-event evidence by a
separately identified judge run. A free-form assertion string or a model's
self-reported success is not executable evidence.

`enforcement/common.json` is the shared critical-safety corpus. It is common
because plan freshness, approval scope, private-context isolation, publication
eligibility, and cleanup reachability do not become weaker in a narrower skill
profile.

The files are schema-validated executable inputs, not evidence. A native
harness driver receives their exact paths through `gds harness eval`, executes
the required samples in an isolated fixture, and returns transcript-bound
evidence. GDS recomputes coverage, metric totals, thresholds, mutation results,
file digests, and the aggregate result before accepting it.

Release acceptance remains blocked until every distributed profile has:

- exact discovery-set evidence;
- explicit invocation evidence;
- at least eight positive and eight near-miss negative trigger queries per
  implicitly routable skill;
- output assertions against a no-skill baseline;
- deterministic enforcement evidence for every critical gate.

An evaluation cannot reduce the corpus, reuse one transcript for multiple
attempts, substitute a model/profile/executable, reference an absolute or
escaping transcript path, or claim a metric count that differs from the exact
sample/run set.
