\encoding UTF8

BEGIN;

INSERT INTO warehouses (name)
VALUES ('Основной склад');

INSERT INTO categories (code, name)
VALUES
    ('tremor', 'TREMOR'),
    ('cutlets', 'КОТЛЕТКИ'),
    ('vases', 'ВАЗЫ');

WITH
    warehouse AS (
        SELECT id
        FROM warehouses
        WHERE name = 'Основной склад'
    ),
    tremor_category AS (
        SELECT id
        FROM categories
        WHERE code = 'tremor'
    )
INSERT INTO cells (
    warehouse_id,
    category_id,
    code,
    row_number,
    position_number
)
SELECT
    warehouse.id,
    tremor_category.id,
    cell_data.code,
    cell_data.row_number,
    cell_data.position_number
FROM (
    VALUES
        ('A1', 1, 1),
        ('A2', 1, 2),
        ('A3', 1, 3),
        ('B1', 2, 1),
        ('B2', 2, 2),
        ('B3', 2, 3),
        ('C1', 3, 1),
        ('C2', 3, 2),
        ('C3', 3, 3)
) AS cell_data (
    code,
    row_number,
    position_number
)
CROSS JOIN warehouse
CROSS JOIN tremor_category;

INSERT INTO products (
    category_id,
    sku,
    name
)
SELECT
    categories.id,
    product_data.sku,
    product_data.name
FROM categories
CROSS JOIN (
    VALUES
        ('TREMOR-CLASSIC', 'Ручка TREMOR Classic'),
        ('TREMOR-PRO', 'Ручка TREMOR Pro'),
        ('TREMOR-MINI', 'Ручка TREMOR Mini'),
        ('TREMOR-MOUNT-KIT', 'Комплект креплений'),
        ('TREMOR-SPARE-BOLTS', 'Запасные болты')
) AS product_data (
    sku,
    name
)
WHERE categories.code = 'tremor';

INSERT INTO cell_stock (
    cell_id,
    product_id,
    quantity
)
SELECT
    cells.id,
    products.id,
    stock_data.quantity
FROM (
    VALUES
        ('A1', 'TREMOR-CLASSIC', 12),
        ('A1', 'TREMOR-PRO', 7),
        ('A1', 'TREMOR-MOUNT-KIT', 24),
        ('A2', 'TREMOR-MINI', 5),
        ('B1', 'TREMOR-SPARE-BOLTS', 42)
) AS stock_data (
    cell_code,
    product_sku,
    quantity
)
JOIN warehouses
    ON warehouses.name = 'Основной склад'
JOIN cells
    ON cells.warehouse_id = warehouses.id
    AND cells.code = stock_data.cell_code
JOIN products
    ON products.sku = stock_data.product_sku;

COMMIT;