BEGIN;

CREATE TABLE receiving_sessions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    completed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE receiving_placements
    ADD COLUMN receiving_session_id BIGINT
        REFERENCES receiving_sessions (id)
        ON DELETE RESTRICT;

CREATE INDEX receiving_placements_receiving_session_id_idx
    ON receiving_placements (receiving_session_id);

COMMIT;