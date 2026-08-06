BEGIN;

CREATE TABLE warehouses (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE categories (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code VARCHAR(64) NOT NULL UNIQUE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE cells (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    warehouse_id BIGINT NOT NULL,
    category_id BIGINT NOT NULL,
    code VARCHAR(32) NOT NULL,
    row_number INTEGER NOT NULL,
    position_number INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT cells_warehouse_fk
        FOREIGN KEY (warehouse_id)
        REFERENCES warehouses (id)
        ON DELETE CASCADE,

    CONSTRAINT cells_category_fk
        FOREIGN KEY (category_id)
        REFERENCES categories (id)
        ON DELETE RESTRICT,

    CONSTRAINT cells_row_number_positive
        CHECK (row_number > 0),

    CONSTRAINT cells_position_number_positive
        CHECK (position_number > 0),

    CONSTRAINT cells_warehouse_code_unique
        UNIQUE (warehouse_id, code),

    CONSTRAINT cells_position_unique
        UNIQUE (
            warehouse_id,
            category_id,
            row_number,
            position_number
        )
);

CREATE TABLE products (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    category_id BIGINT NOT NULL,
    sku VARCHAR(128) NOT NULL UNIQUE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT products_category_fk
        FOREIGN KEY (category_id)
        REFERENCES categories (id)
        ON DELETE RESTRICT
);

CREATE TABLE cell_stock (
    cell_id BIGINT NOT NULL,
    product_id BIGINT NOT NULL,
    quantity INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT cell_stock_pk
        PRIMARY KEY (cell_id, product_id),

    CONSTRAINT cell_stock_cell_fk
        FOREIGN KEY (cell_id)
        REFERENCES cells (id)
        ON DELETE CASCADE,

    CONSTRAINT cell_stock_product_fk
        FOREIGN KEY (product_id)
        REFERENCES products (id)
        ON DELETE CASCADE,

    CONSTRAINT cell_stock_quantity_non_negative
        CHECK (quantity >= 0)
);

CREATE INDEX cells_warehouse_id_idx
    ON cells (warehouse_id);

CREATE INDEX cells_category_id_idx
    ON cells (category_id);

CREATE INDEX products_category_id_idx
    ON products (category_id);

CREATE INDEX cell_stock_product_id_idx
    ON cell_stock (product_id);

COMMIT;