BEGIN;
ALTER TABLE cells DROP CONSTRAINT cells_warehouse_code_unique;
ALTER TABLE cells ADD CONSTRAINT cells_rack_code_unique UNIQUE (warehouse_id, category_id, code);
DELETE FROM categories
WHERE (code = 'cutlets' AND name = 'КОТЛЕТКИ' OR code = 'vases' AND name = 'ВАЗЫ')
AND NOT EXISTS (SELECT 1 FROM products WHERE products.category_id = categories.id)
AND NOT EXISTS (SELECT 1 FROM cells WHERE cells.category_id = categories.id);
COMMIT;
