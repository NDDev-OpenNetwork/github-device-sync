# Runbook: reconciling the default-branch ruleset

Status: local implementation runbook. Applying mutates live branch protection on
the selected repository; read the whole document before the first apply.

`gds github ruleset` is the only supported way to change the tracked
default-branch ruleset. Editing it in the GitHub UI leaves the tracked contract
and live state disagreeing with nothing to reconcile them.

## What the command owns, and what it must not touch

When a desired `required_status_checks` rule is present, GDS owns its contents
wholesale. An omitted rule is preserved unless the compiled delivery profile
projects explicit `remove_required_status_checks` intent into the plan.

The repository-owned baseline is `.github/rulesets/branch-main.json`.
`requirements/external-required-checks.json` adds contexts with declared owners
to that baseline. A duplicate generated context or an entry without a context
name and owner is rejected.

Selecting `continuous-development` deliberately removes the entire required
status-check rule from the selected repository ruleset, including declared
external contexts. Review the plan's explicit removal flag and exact before/after
state. PR/signature rules, other rulesets, classic protection and environment
gates remain separate contracts; profile selection alone performs no write.

Before any apply, read the plan's own diff rather than trusting either file:

```bash
python3 - <<'PY'
import json
d = json.load(open(PLAN_JSON))['data']['plan']
p = d['steps'][0]['parameters']['github_ruleset']
def ctx(o):
    for r in o.get('rules') or []:
        if r.get('type') == 'required_status_checks':
            return sorted(c['context'] for c in r.get('required_status_checks') or [])
    return []
live, desired = ctx(p.get('expected') or {}), ctx(p.get('desired') or {})
print('ADDED  :', sorted(set(desired) - set(live)))
print('REMOVED:', sorted(set(live) - set(desired)))
PY
```

A `REMOVED` entry you did not intend is the signal to stop. Branch protection
lost this way reports nothing; the only evidence is its absence.

## Preconditions

- `--estate-root` selects the verified control-plane used for both provider
  inventory and canonical policy. It overrides environment/registered estate
  selection for this operation; the precondition observer retains that root.
- The mutation runtime must be configured. `gds context --json` reporting
  `capabilities.mutations.runtime: configuration-required` means the GitHub App
  credential does not resolve on this device, and the apply will fail *after* its
  preconditions verify, leaving live state untouched. Read access is independent:
  planning can observe the live ruleset while applying cannot write it.
- `GDS_TRUST_POLICY_FILE` must point at the operation-approval trust policy.
- The owner's Ed25519 approval key must be available.

## The four steps

Planning is side-effect free and doubles as the drift report — it stores a plan
only when live and desired differ.

```bash
export GDS_TRUST_POLICY_FILE="$TRUST/operation-approval-trust-policy.json"
COMMON=(--installation installation:github-organization
        --owner example-org --repository github-device-sync
        --estate-root "$ROOT" --device-id "$DEVICE" --session-id "$SESSION"
        --runtime-config "$CONFIG/github-runtime.yaml"
        --mutation-runtime-config "$CONFIG/github-mutation-runtime.yaml")

gds github ruleset --plan "${COMMON[@]}" --json     # -> plan_...
gds operation approve "$PLAN" --state-path "$STATE" \
  --actor-id owner:<login> --actor-type owner \
  --key-id <key-id> --private-key "$TRUST/<key>.pem" \
  --ttl 20m --output "$APPROVAL" --json
gds operation enable "$PLAN" --state-path "$STATE" \
  --approval-file "$APPROVAL" --device-id "$DEVICE" --session-id "$SESSION" --json
gds github ruleset --apply "$PLAN" --approval-ref "$APPROVAL" "${COMMON[@]}" --json
```

`--state-path` is required on `approve` and `enable`; they do not fall back to
the XDG state database the way the other commands do.

Enablement is one-shot. A failed apply consumes it, so every retry needs a fresh
plan, approval and enablement — in that order, with no commits in between.

## Failure modes seen in practice

**`GDS_STALE_PLAN`, mutation not attempted.** The plan's preconditions no longer
match what the engine re-observes. Every precondition field is compared by exact
equality, including `head_oid`, so *committing anything between plan and apply
invalidates the plan*. This is the common cause and it is not a defect.

**`GDS_APPROVAL_SIGNATURE_INVALID`.** The approval names a different plan. Almost
always a stale approval file left from an earlier attempt — delete it before
re-approving rather than overwriting.

**`GDS_OPERATION_STEP_FAILED`, mutation attempted, not completed.** Preconditions
verified and the handler was called. The operation journal records the step
failure. Check `gds context` for `mutations.runtime` and read the live ruleset
again before deciding whether to retry. A failed response or post-write read
can follow an accepted provider update; failure alone does not prove unchanged
live state. Never blindly repeat the write.

Read any operation's journal with:

```bash
gds operation inspect "$OPERATION_ID" --state-path "$STATE" --json
```

The `preconditions-stale` event names the exact field that differed, which is
faster than re-deriving it.

## Renaming a required context

A workflow change that renames a check, such as changing a matrix value used in
the job name, needs the live ruleset
swapped **while the pull request is open**, not after it merges:

1. open the pull request carrying the workflow and tracked-ruleset change;
2. apply the ruleset reconcile from that branch, so the desired state is the new
   contract;
3. the pull request's own run emits the new context and satisfies the new
   requirement, so it can merge.

Merging first strands every open pull request on a check that can no longer run.
Applying first without the branch open does the same to the pull request you are
trying to land. Other open pull requests will need a branch update afterwards.

## Verification

`gds github ruleset --verify "$OPERATION_ID"` re-reads live state for a completed
operation. To observe drift without a provider write, use the same configured
runtime and exact repository options:

```bash
gds github ruleset --plan "${COMMON[@]}" --json
```

This may store a local plan when drift exists. It does not apply that plan;
provider writing still requires its signed approval and one-shot enablement.
