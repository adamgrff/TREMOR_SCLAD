BEGIN;

ALTER TABLE receiving_sessions
    ADD COLUMN started_at TIMESTAMPTZ;

UPDATE receiving_sessions
SET started_at = completed_at;

ALTER TABLE receiving_sessions
    ALTER COLUMN started_at SET DEFAULT NOW(),
    ALTER COLUMN started_at SET NOT NULL,
    ALTER COLUMN completed_at DROP NOT NULL,
    ADD COLUMN status TEXT NOT NULL DEFAULT 'completed';

ALTER TABLE receiving_sessions
    ADD CONSTRAINT receiving_sessions_status_valid
        CHECK (status IN ('active', 'completed')),
    ADD CONSTRAINT receiving_sessions_state_valid
        CHECK (
            (status = 'active' AND completed_at IS NULL)
            OR
            (status = 'completed' AND completed_at IS NOT NULL)
        );

CREATE TABLE receiving_session_items (
    session_id BIGINT NOT NULL
        REFERENCES receiving_sessions (id)
        ON DELETE CASCADE,
    product_id BIGINT NOT NULL
        REFERENCES products (id)
        ON DELETE RESTRICT,
    quantity INTEGER NOT NULL,

    CONSTRAINT receiving_session_items_pk
        PRIMARY KEY (session_id, product_id),

    CONSTRAINT receiving_session_items_quantity_positive
        CHECK (quantity > 0)
);

CREATE INDEX receiving_session_items_product_id_idx
    ON receiving_session_items (product_id);

COMMIT;