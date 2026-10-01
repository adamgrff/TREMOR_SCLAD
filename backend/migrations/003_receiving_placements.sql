BEGIN;

CREATE TABLE receiving_placements (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    cell_id BIGINT NOT NULL
        REFERENCES cells (id)
        ON DELETE RESTRICT,

    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    undone_at TIMESTAMPTZ,

    CONSTRAINT receiving_placements_status_valid
        CHECK (status IN ('active', 'undone')),

    CONSTRAINT receiving_placements_undo_state_valid
        CHECK (
            (status = 'active' AND undone_at IS NULL)
            OR
            (status = 'undone' AND undone_at IS NOT NULL)
        )
);

CREATE TABLE receiving_placement_items (
    placement_id BIGINT NOT NULL
        REFERENCES receiving_placements (id)
        ON DELETE RESTRICT,
    product_id BIGINT NOT NULL
        REFERENCES products (id)
        ON DELETE RESTRICT,
    quantity INTEGER NOT NULL,

    CONSTRAINT receiving_placement_items_pk
        PRIMARY KEY (placement_id, product_id),

    CONSTRAINT receiving_placement_items_quantity_positive
        CHECK (quantity > 0)
);

CREATE INDEX receiving_placement_items_product_id_idx
    ON receiving_placement_items (product_id);

COMMIT;