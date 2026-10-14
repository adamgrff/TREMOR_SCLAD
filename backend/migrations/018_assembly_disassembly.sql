BEGIN;
CREATE TABLE assembly_disassembly_sessions (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 order_id UUID NOT NULL UNIQUE REFERENCES assembly_order_records(id),
 problem_id UUID NOT NULL REFERENCES assembly_problem_records(id),
 status TEXT NOT NULL DEFAULT 'waiting' CHECK(status IN ('waiting','active','completed')),
 snapshot JSONB NOT NULL,
 pending_item_id UUID,
 last_scan TEXT NOT NULL DEFAULT '—',
 log TEXT NOT NULL DEFAULT '',
 error BOOLEAN NOT NULL DEFAULT false,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 started_at TIMESTAMPTZ,
 completed_at TIMESTAMPTZ,
 CHECK(status<>'completed' OR (pending_item_id IS NULL AND completed_at IS NOT NULL))
);
CREATE UNIQUE INDEX assembly_disassembly_one_active ON assembly_disassembly_sessions((true)) WHERE status='active';
CREATE TABLE assembly_disassembly_items (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 session_id UUID NOT NULL REFERENCES assembly_disassembly_sessions(id),
 original_operation_id UUID NOT NULL UNIQUE REFERENCES assembly_warehouse_operations(id),
 ordinal INTEGER NOT NULL,
 returnable BOOLEAN NOT NULL,
 payload JSONB NOT NULL,
 UNIQUE(session_id,ordinal), UNIQUE(id,session_id)
);
ALTER TABLE assembly_disassembly_sessions ADD FOREIGN KEY(pending_item_id,id) REFERENCES assembly_disassembly_items(id,session_id);
CREATE TABLE assembly_disassembly_returns (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 request_id UUID NOT NULL UNIQUE REFERENCES assembly_preview_requests(request_id) DEFERRABLE INITIALLY DEFERRED,
 item_id UUID NOT NULL UNIQUE REFERENCES assembly_disassembly_items(id),
 product_id BIGINT NOT NULL REFERENCES products(id),
 cell_id BIGINT NOT NULL REFERENCES cells(id),
 quantity INTEGER NOT NULL CHECK(quantity=1),
 occurred_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER assembly_disassembly_items_immutable BEFORE UPDATE OR DELETE ON assembly_disassembly_items FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
CREATE TRIGGER assembly_disassembly_returns_immutable BEFORE UPDATE OR DELETE ON assembly_disassembly_returns FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
CREATE FUNCTION assembly_validate_disassembly_return() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM assembly_disassembly_items i JOIN assembly_disassembly_sessions s ON s.id=i.session_id
 JOIN assembly_warehouse_operations original ON original.id=i.original_operation_id
 WHERE i.id=NEW.item_id AND i.returnable AND s.status='active' AND s.pending_item_id=i.id
 AND original.kind='pick' AND original.order_id=s.order_id AND original.product_id=NEW.product_id AND original.cell_id=NEW.cell_id
 AND NOT EXISTS(SELECT 1 FROM assembly_warehouse_operations undo WHERE undo.reverses_operation_id=original.id)) THEN
 RAISE EXCEPTION 'Return must confirm its pending eligible original pick and source cell' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER assembly_disassembly_returns_validate BEFORE INSERT ON assembly_disassembly_returns FOR EACH ROW EXECUTE FUNCTION assembly_validate_disassembly_return();
CREATE FUNCTION assembly_disassembly_session_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id<>OLD.id OR NEW.order_id<>OLD.order_id OR NEW.problem_id<>OLD.problem_id OR NEW.snapshot<>OLD.snapshot OR NEW.created_at<>OLD.created_at OR OLD.status='completed' THEN
 RAISE EXCEPTION 'Disassembly source context is immutable and completed session cannot reopen' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER assembly_disassembly_sessions_identity BEFORE UPDATE ON assembly_disassembly_sessions FOR EACH ROW EXECUTE FUNCTION assembly_disassembly_session_identity();
CREATE FUNCTION assembly_disassembly_no_undo_returned() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.kind='undo_pick' AND EXISTS(SELECT 1 FROM assembly_disassembly_items i JOIN assembly_disassembly_returns r ON r.item_id=i.id WHERE i.original_operation_id=NEW.reverses_operation_id) THEN
 RAISE EXCEPTION 'A physically returned pick cannot be undone again' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER assembly_disassembly_warehouse_guard BEFORE INSERT ON assembly_warehouse_operations FOR EACH ROW EXECUTE FUNCTION assembly_disassembly_no_undo_returned();
CREATE FUNCTION assembly_load_disassembly() RETURNS JSONB LANGUAGE sql STABLE AS $$
 SELECT COALESCE(jsonb_agg(jsonb_build_object('id',s.id,'number',o.order_number,'problemId',s.problem_id,'status',s.status,
 'reason',s.snapshot->>'reason','createdAt',s.created_at,'startedAt',s.started_at,'pendingItemId',s.pending_item_id,'lastScan',s.last_scan,'log',s.log,'error',s.error,
 'items',COALESCE((SELECT jsonb_agg(i.payload||jsonb_build_object('id',i.id,'operationId',i.original_operation_id,'returnable',i.returnable,
 'warehouseSku',p.sku,'cell',c.code,'cellId',op.cell_id,'warehouse',w.name,'returned',r.id IS NOT NULL,'returnId',r.id,'returnedAt',r.occurred_at) ORDER BY i.ordinal)
 FROM assembly_disassembly_items i JOIN assembly_warehouse_operations op ON op.id=i.original_operation_id JOIN products p ON p.id=op.product_id
 JOIN cells c ON c.id=op.cell_id JOIN warehouses w ON w.id=c.warehouse_id LEFT JOIN assembly_disassembly_returns r ON r.item_id=i.id WHERE i.session_id=s.id),'[]'::jsonb)) ORDER BY s.created_at,s.id),'[]'::jsonb)
 FROM assembly_disassembly_sessions s JOIN assembly_order_records o ON o.id=s.order_id
$$;
ALTER FUNCTION assembly_load_workspace(JSONB) RENAME TO assembly_load_workspace_v7;
CREATE FUNCTION assembly_load_workspace(compact JSONB) RETURNS JSONB LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN compact IS NULL OR compact='null'::jsonb THEN assembly_load_workspace_v7(compact)
 ELSE assembly_load_workspace_v7(compact)||jsonb_build_object('disassemblySessions',assembly_load_disassembly()) END
$$;
ALTER FUNCTION assembly_store_workspace(JSONB,BOOLEAN) RENAME TO assembly_store_workspace_v7;
CREATE FUNCTION assembly_store_workspace(raw JSONB, importing BOOLEAN DEFAULT FALSE) RETURNS JSONB LANGUAGE plpgsql AS $$
DECLARE compact JSONB;
BEGIN
 compact:=assembly_store_workspace_v7(raw-'disassemblySessions',importing);
 UPDATE assembly_order_records o SET status=CASE s.status WHEN 'waiting' THEN 'awaiting_disassembly' WHEN 'active' THEN 'disassembling' ELSE 'disassembled' END
 FROM assembly_disassembly_sessions s WHERE s.order_id=o.id;
 RETURN compact;
END $$;
COMMIT;
