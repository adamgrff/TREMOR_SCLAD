BEGIN;
CREATE TABLE receiving_quantity_requests (
 request_id TEXT PRIMARY KEY,
 payload TEXT NOT NULL,
 product_id BIGINT NOT NULL REFERENCES products(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
COMMIT;
