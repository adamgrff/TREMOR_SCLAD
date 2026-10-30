BEGIN;

CREATE TABLE receiving_operation_requests (
    request_id UUID PRIMARY KEY,
    operation TEXT NOT NULL CHECK (operation IN ('create-session', 'place', 'undo-placement', 'finish', 'delete-item')),
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    response JSONB NOT NULL CHECK (jsonb_typeof(response) = 'object'),
    status_code INTEGER NOT NULL CHECK (status_code BETWEEN 200 AND 299),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

ALTER TABLE receiving_sessions
    ADD COLUMN pending_revision BIGINT NOT NULL DEFAULT 0 CHECK (pending_revision >= 0);

-- Every writer, including the existing quantity/catalog API, participates.
-- This revision is opaque: a multi-item command may advance it several times.
CREATE FUNCTION receiving_advance_pending_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        UPDATE receiving_sessions SET pending_revision = pending_revision + 1 WHERE id = OLD.session_id;
    ELSE
        UPDATE receiving_sessions SET pending_revision = pending_revision + 1 WHERE id = NEW.session_id;
        IF TG_OP = 'UPDATE' AND OLD.session_id <> NEW.session_id THEN
            UPDATE receiving_sessions SET pending_revision = pending_revision + 1 WHERE id = OLD.session_id;
        END IF;
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER receiving_pending_revision
AFTER INSERT OR UPDATE OR DELETE ON receiving_session_items
FOR EACH ROW EXECUTE FUNCTION receiving_advance_pending_revision();

COMMIT;
