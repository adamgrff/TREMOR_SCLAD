BEGIN;

ALTER TABLE receiving_sessions ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE receiving_sessions ADD CONSTRAINT receiving_sessions_deletion_state_valid
    CHECK (deleted_at IS NULL OR status = 'completed');

COMMIT;
