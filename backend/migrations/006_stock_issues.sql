BEGIN;

CREATE TABLE stock_issues (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id UUID NOT NULL UNIQUE,
    cell_id BIGINT NOT NULL REFERENCES cells (id) ON DELETE RESTRICT,
    product_id BIGINT NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'undone')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    undone_at TIMESTAMPTZ,
    CONSTRAINT stock_issues_undo_state_valid CHECK (
        (status = 'active' AND undone_at IS NULL)
        OR (status = 'undone' AND undone_at IS NOT NULL)
    )
);

CREATE INDEX stock_issues_latest_active_idx ON stock_issues (id DESC) WHERE status = 'active';
CREATE INDEX stock_issues_product_id_idx ON stock_issues (product_id);

COMMIT;
