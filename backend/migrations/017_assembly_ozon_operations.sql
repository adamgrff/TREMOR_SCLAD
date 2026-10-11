BEGIN;
-- Internal domain journal only. No Ozon endpoint or wire schema is assumed here.
CREATE TABLE assembly_ozon_operations (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 order_id UUID NOT NULL REFERENCES assembly_order_records(id),
 order_version_id UUID NOT NULL,
 purpose TEXT NOT NULL CHECK(purpose IN ('confirm_shipment','refresh_order')),
 business_key UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
 payload JSONB NOT NULL CHECK(jsonb_typeof(payload)='object'),
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(order_version_id,order_id) REFERENCES assembly_order_versions(id,order_id),
 UNIQUE(id,payload_hash),
 UNIQUE(order_id,purpose,payload_hash)
);
CREATE TABLE assembly_ozon_reconciliations (
 request_id UUID PRIMARY KEY REFERENCES assembly_preview_requests(request_id) DEFERRABLE INITIALLY DEFERRED,
 operation_id UUID NOT NULL REFERENCES assembly_ozon_operations(id),
 kind TEXT NOT NULL CHECK(kind IN ('requested','unavailable')),
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE assembly_ozon_problem_links (
 problem_id UUID PRIMARY KEY REFERENCES assembly_problem_records(id),
 operation_id UUID NOT NULL REFERENCES assembly_ozon_operations(id)
);
CREATE TRIGGER assembly_ozon_problem_links_immutable BEFORE UPDATE OR DELETE ON assembly_ozon_problem_links FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
CREATE TABLE assembly_ozon_observations (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 operation_id UUID NOT NULL REFERENCES assembly_ozon_operations(id),
 evidence_id TEXT NOT NULL UNIQUE CHECK(length(evidence_id)>0),
 outcome TEXT NOT NULL CHECK(outcome IN ('unknown','not_applied','applied','rejected','cancelled')),
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 evidence JSONB NOT NULL CHECK(jsonb_typeof(evidence)='object'),
 observed_at TIMESTAMPTZ NOT NULL,
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(operation_id,payload_hash) REFERENCES assembly_ozon_operations(id,payload_hash)
);
CREATE INDEX assembly_ozon_observation_order ON assembly_ozon_observations(operation_id,observed_at DESC,recorded_at DESC,id);
CREATE TRIGGER assembly_ozon_operations_immutable BEFORE UPDATE OR DELETE ON assembly_ozon_operations FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
CREATE TRIGGER assembly_ozon_reconciliations_immutable BEFORE UPDATE OR DELETE ON assembly_ozon_reconciliations FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
CREATE TRIGGER assembly_ozon_observations_immutable BEFORE UPDATE OR DELETE ON assembly_ozon_observations FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
-- Closed problems stay in the original storage/history; a terminal outcome is not a return to picking.
ALTER FUNCTION assembly_store_workspace(JSONB,BOOLEAN) RENAME TO assembly_store_workspace_v6;
CREATE FUNCTION assembly_store_workspace(raw JSONB, importing BOOLEAN DEFAULT FALSE) RETURNS JSONB LANGUAGE plpgsql AS $$
DECLARE compact JSONB; entry_row RECORD;
BEGIN
 compact:=assembly_store_workspace_v6(raw,importing);
 FOR entry_row IN SELECT id,order_id,problem_metadata FROM assembly_problem_records WHERE problem_metadata ? 'ozonOperationId' LOOP
  IF NOT EXISTS(SELECT 1 FROM assembly_ozon_operations o WHERE o.id=(entry_row.problem_metadata->>'ozonOperationId')::uuid AND o.order_id=entry_row.order_id) THEN
   RAISE EXCEPTION 'Problem and Ozon operation owners differ' USING ERRCODE='23514';
  END IF;
  INSERT INTO assembly_ozon_problem_links(problem_id,operation_id) VALUES(entry_row.id,(entry_row.problem_metadata->>'ozonOperationId')::uuid) ON CONFLICT DO NOTHING;
  IF NOT EXISTS(SELECT 1 FROM assembly_ozon_problem_links WHERE problem_id=entry_row.id AND operation_id=(entry_row.problem_metadata->>'ozonOperationId')::uuid) THEN
   RAISE EXCEPTION 'Original Ozon operation cannot be replaced' USING ERRCODE='23514';
  END IF;
 END LOOP;
 UPDATE assembly_order_records o SET status='cancelled'
 WHERE EXISTS(SELECT 1 FROM assembly_problem_records p WHERE p.order_id=o.id AND p.problem_type='cancelled_order'
 AND p.resolution_status='resolved' AND p.problem_metadata->>'closedOutcome'='cancelled');
 RETURN compact;
END $$;
COMMIT;
