BEGIN;

CREATE TABLE assembly_problem_contexts (
 problem_id UUID PRIMARY KEY REFERENCES assembly_problem_records(id),
 source TEXT NOT NULL CHECK(source IN ('queue','assembly')),
 complete BOOLEAN NOT NULL,
 snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE FUNCTION assembly_capture_problem_context(pid UUID, raw JSONB) RETURNS VOID LANGUAGE plpgsql AS $$
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
  INSERT INTO assembly_cell_references(display_code) VALUES(raw->>'confirmedCell') ON CONFLICT(scope,display_code) DO UPDATE SET display_code=EXCLUDED.display_code RETURNING id INTO confirmed;
 END IF;
 IF raw->>'suggestedCell' IS NOT NULL THEN
  INSERT INTO assembly_cell_references(display_code) VALUES(raw->>'suggestedCell') ON CONFLICT(scope,display_code) DO UPDATE SET display_code=EXCLUDED.display_code RETURNING id INTO suggested;
 END IF;
 snapshot:=snapshot || jsonb_build_object('confirmedCellReferenceId',confirmed,'suggestedCellReferenceId',suggested);
 snapshot:=snapshot || jsonb_build_object('affectedPositions',COALESCE((SELECT jsonb_agg(value ORDER BY ordinal) FROM jsonb_array_elements(positions) WITH ORDINALITY x(value,ordinal) WHERE EXISTS(SELECT 1 FROM assembly_problem_affected_positions a WHERE a.problem_id=pid AND a.sku=value->>'sku')),'[]'::jsonb));
 INSERT INTO assembly_problem_contexts(problem_id,source,complete,snapshot) VALUES(pid,origin,(raw->>'complete')::boolean,snapshot);
END $$;

SELECT assembly_capture_problem_context(id,NULL) FROM assembly_problem_records;
CREATE TRIGGER assembly_context_no_rewrite BEFORE UPDATE OR DELETE ON assembly_problem_contexts
 FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();

ALTER FUNCTION assembly_load_problem(UUID) RENAME TO assembly_load_problem_v2;
CREATE FUNCTION assembly_load_problem(pid UUID) RETURNS JSONB LANGUAGE sql STABLE AS $$
 SELECT assembly_load_problem_v2(pid) || jsonb_build_object('problem',
 assembly_load_problem_v2(pid)->'problem' || jsonb_build_object('creationContext',snapshot))
 FROM assembly_problem_contexts WHERE problem_id=pid
$$;

ALTER FUNCTION assembly_store_workspace(JSONB,BOOLEAN) RENAME TO assembly_store_workspace_v2;
CREATE FUNCTION assembly_store_workspace(raw JSONB, importing BOOLEAN DEFAULT FALSE) RETURNS JSONB LANGUAGE plpgsql AS $$
DECLARE entry JSONB; pid UUID; previous UUID[]; compact JSONB;
BEGIN
 PERFORM 1 FROM assembly_preview_drafts WHERE id=true FOR UPDATE;
 SELECT COALESCE(array_agg(id),'{}') INTO previous FROM assembly_problem_records;
 FOR entry IN SELECT value FROM jsonb_array_elements(COALESCE(raw->'problemOrders','[]')) LOOP
  IF entry ? 'problemId' AND EXISTS(SELECT 1 FROM assembly_problem_contexts WHERE problem_id=(entry->>'problemId')::uuid AND snapshot IS DISTINCT FROM entry->'problem'->'creationContext') THEN
   RAISE EXCEPTION 'Creation context is immutable' USING ERRCODE='23514';
  END IF;
 END LOOP;
 compact:=assembly_store_workspace_v2(raw,importing);
 FOR pid IN SELECT id FROM assembly_problem_records WHERE NOT(id=ANY(previous)) LOOP
  IF (SELECT creation_problem_metadata->'creationContext' FROM assembly_problem_records WHERE id=pid) IS NULL THEN
   RAISE EXCEPTION 'Full creation context required' USING ERRCODE='23514';
  END IF;
  PERFORM assembly_capture_problem_context(pid,(SELECT creation_problem_metadata->'creationContext' FROM assembly_problem_records WHERE id=pid));
 END LOOP;
 RETURN compact;
END $$;
COMMIT;
