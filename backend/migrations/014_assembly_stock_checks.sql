BEGIN;
CREATE TABLE assembly_order_reservations (
 order_id UUID NOT NULL REFERENCES assembly_order_records(id),
 product_id BIGINT NOT NULL REFERENCES products(id),
 quantity INTEGER NOT NULL CHECK(quantity>=0),
 status TEXT NOT NULL CHECK(status IN ('held','released')),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(order_id,product_id),
 CHECK(status='held' OR quantity=0)
);
CREATE INDEX assembly_reservations_product ON assembly_order_reservations(product_id) WHERE status='held';
-- Preview picks do not decrease cell_stock. Keep their temporary stock hold separate
-- from the remaining reservation until real warehouse operations replace the preview.
CREATE TABLE assembly_preview_stock_holds (
 order_id UUID NOT NULL REFERENCES assembly_order_records(id),
 product_id BIGINT NOT NULL REFERENCES products(id),
 quantity INTEGER NOT NULL CHECK(quantity>=0),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(order_id,product_id)
);
CREATE TABLE assembly_stock_checks (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 order_id UUID NOT NULL REFERENCES assembly_order_records(id),
 request_id UUID NOT NULL UNIQUE,
 purpose TEXT NOT NULL CHECK(purpose IN ('check-stock','return','start','preview-progress')),
 result JSONB NOT NULL,
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER assembly_checks_no_rewrite BEFORE UPDATE OR DELETE ON assembly_stock_checks
 FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
-- Other causes require evidence from their future server operations. A legacy
-- 'resolved' label or enough stock is not physical verification / SKU mapping.
CREATE TABLE assembly_problem_clearances (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 problem_id UUID NOT NULL REFERENCES assembly_problem_records(id),
 order_version_id UUID NOT NULL REFERENCES assembly_order_versions(id),
 kind TEXT NOT NULL CHECK(kind IN ('physical_check','damage_handled','sku_mapping','order_verified','manual_resolution')),
 evidence JSONB NOT NULL CHECK(jsonb_typeof(evidence)='object' AND evidence<>'{}'::jsonb),
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER assembly_clearances_no_rewrite BEFORE UPDATE OR DELETE ON assembly_problem_clearances
 FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();

ALTER FUNCTION assembly_store_workspace(JSONB,BOOLEAN) RENAME TO assembly_store_workspace_v3;
CREATE FUNCTION assembly_store_workspace(raw JSONB, importing BOOLEAN DEFAULT FALSE) RETURNS JSONB LANGUAGE plpgsql AS $$
DECLARE entry JSONB;
BEGIN
 PERFORM 1 FROM assembly_preview_drafts WHERE id=true FOR UPDATE;
 IF current_setting('assembly.server_action',true) IS DISTINCT FROM 'true' THEN
  FOR entry IN SELECT value FROM jsonb_array_elements(COALESCE(raw->'problemOrders','[]')) LOOP
   IF entry IS DISTINCT FROM assembly_load_problem((entry->>'problemId')::uuid) THEN
    RAISE EXCEPTION 'Problem changes require server action' USING ERRCODE='23514';
   END IF;
  END LOOP;
  IF raw->'activeOrder' IS DISTINCT FROM (SELECT assembly_load_workspace(state)->'activeOrder' FROM assembly_preview_drafts WHERE id=true)
   AND raw->'activeOrder' <> 'null'::jsonb THEN
   RAISE EXCEPTION 'Starting or returning order requires server check' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN assembly_store_workspace_v3(raw,importing);
END $$;
COMMIT;
