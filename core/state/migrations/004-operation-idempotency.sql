ALTER TABLE operation_steps
    ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT '';

UPDATE operation_steps
SET idempotency_key = 'legacy:' || operation_id || ':' || step_id
WHERE idempotency_key = '';

CREATE UNIQUE INDEX operation_steps_idempotency_key
    ON operation_steps(idempotency_key);

CREATE TRIGGER operation_steps_idempotency_required
BEFORE INSERT ON operation_steps
WHEN NEW.idempotency_key = ''
BEGIN
    SELECT RAISE(ABORT, 'operation step idempotency key is required');
END;

CREATE TRIGGER operation_steps_idempotency_immutable
BEFORE UPDATE OF idempotency_key ON operation_steps
BEGIN
    SELECT RAISE(ABORT, 'operation step idempotency key is immutable');
END;

ALTER TABLE reconciliation_runs
    ADD COLUMN cursor_sequence INTEGER NOT NULL DEFAULT 0;

ALTER TABLE reconciliation_runs
    ADD COLUMN cursor_digest TEXT NOT NULL DEFAULT '';
