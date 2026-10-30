BEGIN;

-- Successful receipts are durable evidence. Rewriting/removing one would
-- allow an already committed command to be counted again on retry.
CREATE FUNCTION receiving_receipt_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'Receiving command receipts are immutable' USING ERRCODE = '23514';
END;
$$;

CREATE TRIGGER receiving_quantity_requests_no_rewrite
BEFORE UPDATE OR DELETE ON receiving_quantity_requests
FOR EACH ROW EXECUTE FUNCTION receiving_receipt_immutable();

CREATE TRIGGER receiving_quantity_requests_no_truncate
BEFORE TRUNCATE ON receiving_quantity_requests
FOR EACH STATEMENT EXECUTE FUNCTION receiving_receipt_immutable();

CREATE TRIGGER receiving_operation_requests_no_rewrite
BEFORE UPDATE OR DELETE ON receiving_operation_requests
FOR EACH ROW EXECUTE FUNCTION receiving_receipt_immutable();

CREATE TRIGGER receiving_operation_requests_no_truncate
BEFORE TRUNCATE ON receiving_operation_requests
FOR EACH STATEMENT EXECUTE FUNCTION receiving_receipt_immutable();

COMMIT;
