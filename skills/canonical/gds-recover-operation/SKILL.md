---
name: gds-recover-operation
description: Use this skill only when the owner explicitly asks to inspect and resume, abort, or compensate an interrupted GDS operation or stale lock. Reconstruct state from the durable journal and current evidence before any action. Do not use it to conceal partial completion or perform a generic rollback.
disable-model-invocation: true
---

# Contract

Recover one interrupted operation without blind retries or history rewriting.

## Use when

- An operation journal is partial, a worker stopped, or a lock may be stale.

## Do not use when

- No operation ID or recoverable journal exists.
- The owner only asks for status.

## Inputs

- Operation, plan, and lock IDs.
- Durable journal, current repository/provider state, and requested recovery mode.

## Preconditions

1. Inspect the journal and every affected object.
2. Determine which steps completed idempotently.
3. Run `gds recover operation <operation-id> --plan` with the exact state path,
   device ID, and session ID.
4. Obtain approval only when the result contains an automatable stored plan.

## Workflow

1. Reconcile recorded before/after evidence with current state.
2. Classify each step as complete, retryable, compensatable, or manual-only.
3. Apply only the approved recovery plan with
   `gds recover operation <operation-id> --apply <plan-id>`.
4. Treat every reported compensation as a separate future plan; never execute
   it while closing the interrupted journal.
5. Verify each affected boundary and close or quarantine the journal.

## Stop conditions

Stop on missing evidence, changed external state, unknown side effects, unsafe
compensation, active lock owner, stale recovery plan, or insufficient approval.

## Verification

Run
`gds recover operation <operation-id> --verify <recovery-operation-id> --json`
and confirm journal closure.

## Output

Return recovered and untouched steps, compensations, current refs/state,
remaining manual actions, and final journal status.

## References

No additional runtime reference is required; the durable journal is authoritative.
