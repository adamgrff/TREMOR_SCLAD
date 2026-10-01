BEGIN;
ALTER TABLE products ADD COLUMN archived BOOLEAN NOT NULL DEFAULT FALSE;
CREATE UNIQUE INDEX products_sku_case_insensitive_unique ON products (UPPER(sku));
COMMIT;
