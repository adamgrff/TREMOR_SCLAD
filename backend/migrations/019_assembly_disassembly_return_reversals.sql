BEGIN;
-- R4.2A: model only. No stock mutation and no user-facing reversal action.
CREATE TABLE assembly_disassembly_return_reversals (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 request_id UUID NOT NULL UNIQUE REFERENCES assembly_preview_requests(request_id) DEFERRABLE INITIALLY DEFERRED,
 return_id UUID NOT NULL UNIQUE REFERENCES assembly_disassembly_returns(id),
 occurred_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER assembly_disassembly_return_reversals_immutable BEFORE UPDATE OR DELETE ON assembly_disassembly_return_reversals
 FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();

-- The sole definition of a currently effective physical return.
CREATE VIEW assembly_disassembly_effective_returns AS
 SELECT r.* FROM assembly_disassembly_returns r
 WHERE NOT EXISTS(SELECT 1 FROM assembly_disassembly_return_reversals reversal WHERE reversal.return_id=r.id);

ALTER TABLE assembly_disassembly_returns DROP CONSTRAINT assembly_disassembly_returns_item_id_key;
CREATE INDEX assembly_disassembly_returns_item ON assembly_disassembly_returns(item_id);

CREATE OR REPLACE FUNCTION assembly_validate_disassembly_return() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- A fixed REPEATABLE READ snapshot could miss a just-committed rival return
 -- after waiting for the item lock. Reject that write mode; history reads may
 -- still use REPEATABLE READ. Existing mutation handlers use READ COMMITTED.
 IF current_setting('transaction_isolation')='repeatable read' THEN
 RAISE EXCEPTION 'Disassembly ledger writes require READ COMMITTED or SERIALIZABLE' USING ERRCODE='0A000'; END IF;
 -- Serialize attempts for the same immutable source unit. The existing write
 -- handlers use READ COMMITTED and also hold the workspace/session locks.
 PERFORM 1 FROM assembly_disassembly_items WHERE id=NEW.item_id FOR UPDATE;
 IF NOT EXISTS(SELECT 1 FROM assembly_disassembly_items i JOIN assembly_disassembly_sessions s ON s.id=i.session_id
 JOIN assembly_warehouse_operations original ON original.id=i.original_operation_id
 WHERE i.id=NEW.item_id AND i.returnable AND s.status='active' AND s.pending_item_id=i.id
 AND original.kind='pick' AND original.order_id=s.order_id AND original.product_id=NEW.product_id AND original.cell_id=NEW.cell_id
 AND NOT EXISTS(SELECT 1 FROM assembly_warehouse_operations undo WHERE undo.reverses_operation_id=original.id)) THEN
 RAISE EXCEPTION 'Return must confirm its pending eligible original pick and source cell' USING ERRCODE='23514';
 END IF;
 IF EXISTS(SELECT 1 FROM assembly_disassembly_effective_returns r WHERE r.item_id=NEW.item_id) THEN
 RAISE EXCEPTION 'Source item already has an effective disassembly return' USING ERRCODE='23505';
 END IF;
 RETURN NEW;
END $$;

-- Only referential/append-only invariants here. Active/pending/last-return guards
-- and the stock compensation belong to R4.2B, not this schema-only stage.
CREATE FUNCTION assembly_validate_disassembly_return_reversal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF current_setting('transaction_isolation')='repeatable read' THEN
 RAISE EXCEPTION 'Disassembly ledger writes require READ COMMITTED or SERIALIZABLE' USING ERRCODE='0A000'; END IF;
 PERFORM 1 FROM assembly_disassembly_items i JOIN assembly_disassembly_returns r ON r.item_id=i.id
 WHERE r.id=NEW.return_id FOR UPDATE OF i;
 IF NOT FOUND THEN
 RAISE EXCEPTION 'Reversal must reference an existing physical return' USING ERRCODE='23503';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER assembly_disassembly_return_reversals_validate BEFORE INSERT ON assembly_disassembly_return_reversals
 FOR EACH ROW EXECUTE FUNCTION assembly_validate_disassembly_return_reversal();

-- This is a current-state safety guard, not an historical existence test.
CREATE OR REPLACE FUNCTION assembly_disassembly_no_undo_returned() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.kind='undo_pick' AND EXISTS(SELECT 1 FROM assembly_disassembly_items i JOIN assembly_disassembly_effective_returns r ON r.item_id=i.id WHERE i.original_operation_id=NEW.reverses_operation_id) THEN
 RAISE EXCEPTION 'A physically returned pick cannot be undone again' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION assembly_load_disassembly() RETURNS JSONB LANGUAGE sql STABLE AS $$
 SELECT COALESCE(jsonb_agg(jsonb_build_object('id',s.id,'number',o.order_number,'problemId',s.problem_id,'status',s.status,
 'reason',s.snapshot->>'reason','createdAt',s.created_at,'startedAt',s.started_at,'pendingItemId',s.pending_item_id,'lastScan',s.last_scan,'log',s.log,'error',s.error,
 'items',COALESCE((SELECT jsonb_agg(i.payload||jsonb_build_object('id',i.id,'operationId',i.original_operation_id,'returnable',i.returnable,
 'warehouseSku',p.sku,'cell',c.code,'cellId',op.cell_id,'warehouse',w.name,'returned',r.id IS NOT NULL,'returnId',r.id,'returnedAt',r.occurred_at) ORDER BY i.ordinal)
 FROM assembly_disassembly_items i JOIN assembly_warehouse_operations op ON op.id=i.original_operation_id JOIN products p ON p.id=op.product_id
 JOIN cells c ON c.id=op.cell_id JOIN warehouses w ON w.id=c.warehouse_id LEFT JOIN assembly_disassembly_effective_returns r ON r.item_id=i.id WHERE i.session_id=s.id),'[]'::jsonb)) ORDER BY s.created_at,s.id),'[]'::jsonb)
 FROM assembly_disassembly_sessions s JOIN assembly_order_records o ON o.id=s.order_id
$$;
COMMIT;
