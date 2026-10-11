BEGIN;
-- Stage 5 adds warehouse adapters to the existing order/problem/reservation model.
CREATE TABLE assembly_warehouse_operations (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 request_id UUID NOT NULL UNIQUE,
 order_id UUID NOT NULL REFERENCES assembly_order_records(id),
 order_position_id UUID NOT NULL REFERENCES assembly_order_positions(id),
 product_id BIGINT NOT NULL REFERENCES products(id),
 cell_id BIGINT NOT NULL REFERENCES cells(id),
 kind TEXT NOT NULL CHECK(kind IN ('pick','undo_pick')),
 quantity INTEGER NOT NULL CHECK(quantity=1),
 reverses_operation_id UUID UNIQUE REFERENCES assembly_warehouse_operations(id),
 occurred_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK((kind='undo_pick')=(reverses_operation_id IS NOT NULL))
);
CREATE TRIGGER assembly_operations_no_rewrite BEFORE UPDATE OR DELETE ON assembly_warehouse_operations
 FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
CREATE FUNCTION assembly_validate_warehouse_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM assembly_order_positions p JOIN assembly_order_versions v ON v.id=p.version_id
 WHERE p.id=NEW.order_position_id AND v.order_id=NEW.order_id AND p.warehouse_product_id=NEW.product_id) THEN
  RAISE EXCEPTION 'Warehouse operation does not match order position' USING ERRCODE='23514';
 END IF;
 IF NEW.kind='undo_pick' AND NOT EXISTS(SELECT 1 FROM assembly_warehouse_operations original
 WHERE original.id=NEW.reverses_operation_id AND original.kind='pick' AND original.order_id=NEW.order_id
 AND original.order_position_id=NEW.order_position_id AND original.cell_id=NEW.cell_id AND original.product_id=NEW.product_id) THEN
  RAISE EXCEPTION 'Undo must match its original pick' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER assembly_operations_validate BEFORE INSERT ON assembly_warehouse_operations
 FOR EACH ROW EXECUTE FUNCTION assembly_validate_warehouse_operation();
ALTER TABLE assembly_pick_records ADD CONSTRAINT assembly_pick_operation_fk FOREIGN KEY(warehouse_operation_id) REFERENCES assembly_warehouse_operations(id);
ALTER TABLE assembly_cell_references ADD CONSTRAINT assembly_real_cell_fk FOREIGN KEY(warehouse_cell_id) REFERENCES cells(id);
-- Imported heads point at the existing immutable order versions, not a parallel order model.
CREATE TABLE assembly_order_heads (
 order_id UUID PRIMARY KEY REFERENCES assembly_order_records(id),
 version_id UUID NOT NULL REFERENCES assembly_order_versions(id),
 imported_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE assembly_order_imports (
 request_id UUID PRIMARY KEY,
 order_id UUID NOT NULL REFERENCES assembly_order_records(id),
 version_id UUID NOT NULL REFERENCES assembly_order_versions(id),
 payload JSONB NOT NULL,
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE assembly_order_versions ADD CONSTRAINT assembly_version_owner_unique UNIQUE(id,order_id);
ALTER TABLE assembly_order_heads ADD CONSTRAINT assembly_head_owner_fk FOREIGN KEY(version_id,order_id) REFERENCES assembly_order_versions(id,order_id);
ALTER TABLE assembly_order_imports ADD CONSTRAINT assembly_import_owner_fk FOREIGN KEY(version_id,order_id) REFERENCES assembly_order_versions(id,order_id);
CREATE TRIGGER assembly_imports_no_rewrite BEFORE UPDATE OR DELETE ON assembly_order_imports
 FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();

CREATE OR REPLACE FUNCTION assembly_store_order(raw JSONB) RETURNS UUID LANGUAGE plpgsql AS $$
DECLARE oid UUID; vid UUID; item JSONB; n INTEGER:=0; origin TEXT;
BEGIN
 IF raw IS NULL OR raw='null'::jsonb THEN RETURN NULL; END IF;
 origin:=COALESCE(raw->>'source','preview');
 INSERT INTO assembly_order_records(source,order_number) VALUES(origin,raw->>'number')
 ON CONFLICT(source,order_number) DO UPDATE SET order_number=EXCLUDED.order_number RETURNING id INTO oid;
 SELECT id INTO vid FROM assembly_order_versions WHERE order_id=oid AND fingerprint=md5(raw::text);
 IF vid IS NOT NULL THEN RETURN vid; END IF;
 INSERT INTO assembly_order_versions(order_id,fingerprint,metadata) VALUES(oid,md5(raw::text),raw-'items') RETURNING id INTO vid;
 FOR item IN SELECT value FROM jsonb_array_elements(raw->'items') LOOP
  INSERT INTO assembly_order_positions(version_id,ordinal,sku,required_quantity,warehouse_product_id,payload)
  VALUES(vid,n,item->>'sku',(item->>'quantity')::integer,(SELECT id FROM products WHERE upper(sku)=upper(item->>'sku') AND NOT archived),item);
  n:=n+1;
 END LOOP;
 RETURN vid;
END $$;

ALTER FUNCTION assembly_store_pick(UUID,JSONB,INTEGER,UUID) RENAME TO assembly_store_preview_pick;
CREATE FUNCTION assembly_store_pick(oid UUID, raw JSONB, n INTEGER, vid UUID) RETURNS UUID LANGUAGE plpgsql AS $$
DECLARE result UUID;
BEGIN
 IF raw->>'operationId' IS NULL THEN
  IF (SELECT source FROM assembly_order_records WHERE id=oid)='ozon' THEN RAISE EXCEPTION 'Real orders require warehouse operations' USING ERRCODE='23514'; END IF;
  RETURN assembly_store_preview_pick(oid,raw,n,vid);
 END IF;
 SELECT p.id INTO result FROM assembly_pick_records p JOIN assembly_warehouse_operations op ON op.id=p.warehouse_operation_id
 JOIN assembly_cell_references c ON c.id=p.cell_reference_id
 WHERE p.order_id=oid AND op.id=(raw->>'operationId')::uuid AND op.kind='pick' AND p.source='warehouse_operation'
 AND p.position_sku=raw->>'sku' AND c.warehouse_cell_id=(raw->>'cellId')::bigint AND c.display_code=raw->>'cell'
 AND NOT EXISTS(SELECT 1 FROM assembly_warehouse_operations undo WHERE undo.reverses_operation_id=op.id);
 IF result IS NULL THEN RAISE EXCEPTION 'Unknown or reversed warehouse pick' USING ERRCODE='23514'; END IF;
 RETURN result;
END $$;
CREATE FUNCTION assembly_pick_payload(pid UUID) RETURNS JSONB LANGUAGE sql STABLE AS $$
 SELECT jsonb_build_object('sku',p.position_sku,'cell',c.display_code) || CASE WHEN p.source='warehouse_operation' THEN
 jsonb_build_object('pickId',p.id,'operationId',p.warehouse_operation_id,'cellId',c.warehouse_cell_id,'occurredAt',p.occurred_at) ELSE '{}'::jsonb END
 FROM assembly_pick_records p JOIN assembly_cell_references c ON c.id=p.cell_reference_id WHERE p.id=pid
$$;
ALTER FUNCTION assembly_load_workspace(JSONB) RENAME TO assembly_load_workspace_v4;
CREATE FUNCTION assembly_load_workspace(compact JSONB) RETURNS JSONB LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN compact->>'storageVersion'='2' THEN jsonb_set(assembly_load_workspace_v4(compact),'{activeState,picks}',
 COALESCE((SELECT jsonb_agg(assembly_pick_payload(pick_id) ORDER BY ordinal) FROM assembly_workspace_pick_links),'[]'::jsonb)) ELSE assembly_load_workspace_v4(compact) END
$$;
ALTER FUNCTION assembly_load_problem(UUID) RENAME TO assembly_load_problem_v4;
CREATE FUNCTION assembly_load_problem(pid UUID) RETURNS JSONB LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN p.assembly_metadata IS NOT NULL THEN jsonb_set(assembly_load_problem_v4(pid),'{assembly,picks}',
 COALESCE((SELECT jsonb_agg(assembly_pick_payload(pick_id) ORDER BY ordinal) FROM assembly_problem_pick_links WHERE problem_id=pid),'[]'::jsonb)) ELSE assembly_load_problem_v4(pid) END
 FROM assembly_problem_records p WHERE p.id=pid
$$;
CREATE OR REPLACE FUNCTION assembly_capture_problem_context(pid UUID, raw JSONB) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE p assembly_problem_records; snapshot JSONB; positions JSONB; picks JSONB; origin TEXT; confirmed UUID; suggested UUID;
BEGIN
 SELECT * INTO STRICT p FROM assembly_problem_records WHERE id=pid;
 origin:=CASE WHEN p.assembly_metadata IS NULL THEN 'queue' ELSE 'assembly' END;
 IF raw IS NULL THEN
  raw:=jsonb_build_object('schemaVersion',1,'complete',false,'source',origin,'availabilitySource','legacy_snapshot',
   'note','Историческая запись: точное состояние сканирования и время старых отборов неизвестны.',
   'activeState',p.assembly_metadata,'positions',COALESCE((SELECT jsonb_agg(payload ORDER BY ordinal) FROM assembly_order_positions WHERE version_id=p.creation_order_version_id),'[]'::jsonb));
 ELSE
  IF raw->>'source' IS DISTINCT FROM origin OR raw->>'complete' IS DISTINCT FROM 'true' OR raw->>'schemaVersion' IS DISTINCT FROM '1' OR jsonb_array_length(raw->'positions')=0 OR jsonb_array_length(raw->'affectedPositions')=0 THEN
   RAISE EXCEPTION 'Full creation context required' USING ERRCODE='23514';
  END IF;
 END IF;
 SELECT COALESCE(jsonb_agg(x.value || jsonb_build_object('orderPositionId',pos.id) ORDER BY x.ordinal),'[]'::jsonb) INTO positions
 FROM jsonb_array_elements(raw->'positions') WITH ORDINALITY x(value,ordinal)
 JOIN assembly_order_positions pos ON pos.version_id=p.creation_order_version_id AND pos.sku=x.value->>'sku';
 SELECT COALESCE(jsonb_agg(jsonb_build_object('pickId',pick.id,'sku',pick.position_sku,'cell',cell.display_code,'cellReferenceId',cell.id,'warehouseCellId',cell.warehouse_cell_id,'warehouseOperationId',pick.warehouse_operation_id,'occurredAt',pick.occurred_at) ORDER BY link.ordinal),'[]'::jsonb) INTO picks
 FROM assembly_problem_pick_links link JOIN assembly_pick_records pick ON pick.id=link.pick_id
 JOIN assembly_cell_references cell ON cell.id=pick.cell_reference_id WHERE link.problem_id=pid;
 snapshot:=raw || jsonb_build_object('positions',positions,'picks',picks,'orderId',p.order_id,'orderVersionId',p.creation_order_version_id);
 IF raw->>'confirmedCell' IS NOT NULL THEN
  IF raw->>'confirmedCellId' IS NOT NULL THEN
   INSERT INTO assembly_cell_references(scope,display_code,warehouse_cell_id) VALUES('warehouse:'||(raw->>'confirmedCellId'),raw->>'confirmedCell',(raw->>'confirmedCellId')::bigint)
   ON CONFLICT(warehouse_cell_id) DO UPDATE SET warehouse_cell_id=EXCLUDED.warehouse_cell_id RETURNING id INTO confirmed;
  ELSE
   INSERT INTO assembly_cell_references(display_code) VALUES(raw->>'confirmedCell') ON CONFLICT(scope,display_code) DO UPDATE SET display_code=EXCLUDED.display_code RETURNING id INTO confirmed;
  END IF;
 END IF;
 IF raw->>'suggestedCell' IS NOT NULL THEN
  IF raw->>'suggestedCellId' IS NOT NULL THEN
   INSERT INTO assembly_cell_references(scope,display_code,warehouse_cell_id) VALUES('warehouse:'||(raw->>'suggestedCellId'),raw->>'suggestedCell',(raw->>'suggestedCellId')::bigint)
   ON CONFLICT(warehouse_cell_id) DO UPDATE SET warehouse_cell_id=EXCLUDED.warehouse_cell_id RETURNING id INTO suggested;
  ELSE
   INSERT INTO assembly_cell_references(display_code) VALUES(raw->>'suggestedCell') ON CONFLICT(scope,display_code) DO UPDATE SET display_code=EXCLUDED.display_code RETURNING id INTO suggested;
  END IF;
 END IF;
 snapshot:=snapshot || jsonb_build_object('confirmedCellReferenceId',confirmed,'suggestedCellReferenceId',suggested);
 snapshot:=snapshot || jsonb_build_object('affectedPositions',COALESCE((SELECT jsonb_agg(value ORDER BY ordinal) FROM jsonb_array_elements(positions) WITH ORDINALITY x(value,ordinal) WHERE EXISTS(SELECT 1 FROM assembly_problem_affected_positions a WHERE a.problem_id=pid AND a.sku=value->>'sku')),'[]'::jsonb));
 INSERT INTO assembly_problem_contexts(problem_id,source,complete,snapshot) VALUES(pid,origin,(raw->>'complete')::boolean,snapshot);
END $$;
COMMIT;
