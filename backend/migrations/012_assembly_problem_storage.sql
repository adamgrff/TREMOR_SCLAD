BEGIN;

-- Stage 2 stores preview data separately. No warehouse balances or reservations are changed.
CREATE TABLE assembly_order_records (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 source TEXT NOT NULL DEFAULT 'preview' CHECK(source IN ('preview','ozon')),
 order_number TEXT NOT NULL CHECK(length(order_number)>0),
 status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','in_progress','problem','completed','cancelled','awaiting_disassembly','disassembling','disassembled')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(source,order_number)
);
CREATE TABLE assembly_order_versions (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 order_id UUID NOT NULL REFERENCES assembly_order_records(id),
 fingerprint TEXT NOT NULL,
 metadata JSONB NOT NULL CHECK(jsonb_typeof(metadata)='object'),
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(order_id,fingerprint)
);
CREATE TABLE assembly_order_positions (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 version_id UUID NOT NULL REFERENCES assembly_order_versions(id),
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 sku TEXT NOT NULL CHECK(length(sku)>0),
 required_quantity INTEGER NOT NULL CHECK(required_quantity>0 AND required_quantity<=10000),
 warehouse_product_id BIGINT,
 payload JSONB NOT NULL CHECK(jsonb_typeof(payload)='object'),
 UNIQUE(version_id,ordinal), UNIQUE(version_id,sku)
);
CREATE TABLE assembly_cell_references (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 scope TEXT NOT NULL DEFAULT 'preview',
 display_code TEXT NOT NULL CHECK(length(display_code)>0),
 warehouse_cell_id BIGINT UNIQUE,
 UNIQUE(scope,display_code)
);
CREATE TABLE assembly_pick_records (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 order_id UUID NOT NULL REFERENCES assembly_order_records(id),
 position_sku TEXT NOT NULL,
 order_position_id UUID NOT NULL REFERENCES assembly_order_positions(id),
 cell_reference_id UUID NOT NULL REFERENCES assembly_cell_references(id),
 source TEXT NOT NULL DEFAULT 'preview_snapshot' CHECK(source IN ('preview_snapshot','warehouse_operation')),
 warehouse_operation_id UUID UNIQUE,
 snapshot_key TEXT NOT NULL,
 quantity INTEGER NOT NULL DEFAULT 1 CHECK(quantity>0),
 occurred_at TIMESTAMPTZ,
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK(source <> 'warehouse_operation' OR (warehouse_operation_id IS NOT NULL AND occurred_at IS NOT NULL)),
 UNIQUE(order_id,snapshot_key)
);
CREATE TABLE assembly_problem_records (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 order_id UUID NOT NULL REFERENCES assembly_order_records(id),
 order_version_id UUID NOT NULL REFERENCES assembly_order_versions(id),
 creation_order_version_id UUID NOT NULL REFERENCES assembly_order_versions(id),
 legacy_key TEXT NOT NULL UNIQUE,
 problem_type TEXT NOT NULL CHECK(problem_type IN ('insufficient_stock','missing_in_cell','unknown_sku','damaged_product','invalid_order','other','cancelled_order','ozon_error')),
 resolution_status TEXT NOT NULL CHECK(resolution_status IN ('awaiting','resolved')),
 disposition TEXT NOT NULL DEFAULT 'problem' CHECK(disposition IN ('problem','returned')),
 requires_disassembly BOOLEAN,
 created_at TIMESTAMPTZ NOT NULL,
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 creation_problem_metadata JSONB NOT NULL,
 problem_metadata JSONB NOT NULL,
 entry_metadata JSONB NOT NULL,
 assembly_metadata JSONB,
 CHECK(disposition <> 'returned' OR resolution_status='resolved')
);
CREATE INDEX assembly_problems_by_order ON assembly_problem_records(order_id,resolution_status,disposition);
CREATE TABLE assembly_problem_affected_positions (
 problem_id UUID NOT NULL REFERENCES assembly_problem_records(id),
 order_position_id UUID REFERENCES assembly_order_positions(id),
 sku TEXT NOT NULL,
 creation_payload JSONB NOT NULL,
 PRIMARY KEY(problem_id,sku)
);
CREATE TABLE assembly_problem_positions (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 problem_id UUID NOT NULL REFERENCES assembly_problem_records(id),
 order_position_id UUID REFERENCES assembly_order_positions(id),
 sku TEXT NOT NULL,
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 required_quantity INTEGER CHECK(required_quantity>=0),
 available_quantity INTEGER CHECK(available_quantity>=0),
 missing_quantity INTEGER CHECK(missing_quantity>=0),
 is_shortage BOOLEAN NOT NULL,
 payload JSONB,
 UNIQUE(problem_id,sku)
);
CREATE TABLE assembly_problem_pick_links (
 problem_id UUID NOT NULL REFERENCES assembly_problem_records(id),
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 pick_id UUID NOT NULL REFERENCES assembly_pick_records(id),
 PRIMARY KEY(problem_id,ordinal), UNIQUE(problem_id,pick_id)
);
CREATE TABLE assembly_problem_events (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 problem_id UUID NOT NULL REFERENCES assembly_problem_records(id),
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 kind TEXT NOT NULL,
 occurred_at TIMESTAMPTZ NOT NULL,
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 payload JSONB NOT NULL CHECK(jsonb_typeof(payload)='object'),
 UNIQUE(problem_id,ordinal)
);
CREATE TABLE assembly_workspace_pick_links (
 ordinal INTEGER PRIMARY KEY CHECK(ordinal>=0),
 pick_id UUID NOT NULL REFERENCES assembly_pick_records(id)
);
CREATE TABLE assembly_workspace_migration_backup (
 id BOOLEAN PRIMARY KEY CHECK(id),
 version BIGINT NOT NULL,
 state JSONB,
 saved_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO assembly_workspace_migration_backup(id,version,state)
 SELECT id,version,state FROM assembly_preview_drafts ON CONFLICT DO NOTHING;

CREATE FUNCTION assembly_events_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Problem history is append-only' USING ERRCODE='23514'; END $$;
CREATE TRIGGER assembly_events_no_rewrite BEFORE UPDATE OR DELETE ON assembly_problem_events
 FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
CREATE TRIGGER assembly_versions_no_rewrite BEFORE UPDATE OR DELETE ON assembly_order_versions
 FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
CREATE TRIGGER assembly_positions_no_rewrite BEFORE UPDATE OR DELETE ON assembly_order_positions
 FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
CREATE TRIGGER assembly_affected_no_rewrite BEFORE UPDATE OR DELETE ON assembly_problem_affected_positions
 FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
CREATE TRIGGER assembly_problem_picks_no_rewrite BEFORE UPDATE OR DELETE ON assembly_problem_pick_links
 FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();

CREATE FUNCTION assembly_store_order(raw JSONB) RETURNS UUID LANGUAGE plpgsql AS $$
DECLARE oid UUID; vid UUID; item JSONB; n INTEGER:=0;
BEGIN
 IF raw IS NULL OR raw='null'::jsonb THEN RETURN NULL; END IF;
 INSERT INTO assembly_order_records(order_number) VALUES(raw->>'number')
 ON CONFLICT(source,order_number) DO UPDATE SET order_number=EXCLUDED.order_number RETURNING id INTO oid;
 SELECT id INTO vid FROM assembly_order_versions WHERE order_id=oid AND fingerprint=md5(raw::text);
 IF vid IS NOT NULL THEN RETURN vid; END IF;
 INSERT INTO assembly_order_versions(order_id,fingerprint,metadata) VALUES(oid,md5(raw::text),raw-'items') RETURNING id INTO vid;
 FOR item IN SELECT value FROM jsonb_array_elements(raw->'items') LOOP
  INSERT INTO assembly_order_positions(version_id,ordinal,sku,required_quantity,payload) VALUES(vid,n,item->>'sku',(item->>'quantity')::integer,item);
  n:=n+1;
 END LOOP;
 RETURN vid;
END $$;
CREATE FUNCTION assembly_load_order(vid UUID) RETURNS JSONB LANGUAGE sql STABLE AS $$
 SELECT metadata || jsonb_build_object('items',COALESCE((SELECT jsonb_agg(payload ORDER BY ordinal) FROM assembly_order_positions WHERE version_id=vid),'[]'::jsonb))
 FROM assembly_order_versions WHERE id=vid
$$;
CREATE FUNCTION assembly_store_pick(oid UUID, raw JSONB, n INTEGER, vid UUID) RETURNS UUID LANGUAGE plpgsql AS $$
DECLARE cid UUID; pid UUID;
BEGIN
 INSERT INTO assembly_cell_references(display_code) VALUES(raw->>'cell') ON CONFLICT(scope,display_code) DO UPDATE SET display_code=EXCLUDED.display_code RETURNING id INTO cid;
 INSERT INTO assembly_pick_records(order_id,position_sku,order_position_id,cell_reference_id,snapshot_key)
 VALUES(oid,raw->>'sku',(SELECT id FROM assembly_order_positions WHERE version_id=vid AND sku=raw->>'sku'),cid,md5(jsonb_build_array(n,raw)::text)) ON CONFLICT(order_id,snapshot_key) DO UPDATE SET snapshot_key=EXCLUDED.snapshot_key RETURNING id INTO pid;
 RETURN pid;
END $$;
CREATE FUNCTION assembly_load_problem(pid UUID) RETURNS JSONB LANGUAGE sql STABLE AS $$
 SELECT entry_metadata || jsonb_build_object('problemId',p.id,'order',assembly_load_order(order_version_id),
 'problem',problem_metadata || jsonb_build_object('shortages',COALESCE((SELECT jsonb_agg(payload ORDER BY ordinal) FROM assembly_problem_positions WHERE problem_id=p.id AND is_shortage),'[]'::jsonb))
 || CASE WHEN problem_metadata ? 'history' THEN jsonb_build_object('history',COALESCE((SELECT jsonb_agg(payload ORDER BY ordinal) FROM assembly_problem_events WHERE problem_id=p.id),'[]'::jsonb)) ELSE '{}'::jsonb END)
 || CASE WHEN assembly_metadata IS NOT NULL THEN jsonb_build_object('assembly',assembly_metadata || jsonb_build_object('picks',COALESCE((SELECT jsonb_agg(jsonb_build_object('sku',r.position_sku,'cell',c.display_code) ORDER BY l.ordinal) FROM assembly_problem_pick_links l JOIN assembly_pick_records r ON r.id=l.pick_id JOIN assembly_cell_references c ON c.id=r.cell_reference_id WHERE l.problem_id=p.id),'[]'::jsonb))) ELSE '{}'::jsonb END
 FROM assembly_problem_records p WHERE p.id=pid
$$;

CREATE FUNCTION assembly_store_workspace(raw JSONB, importing BOOLEAN DEFAULT FALSE) RETURNS JSONB LANGUAGE plpgsql AS $$
DECLARE entry JSONB; problem JSONB; event JSONB; old_event JSONB; pos JSONB; pick JSONB;
 vid UUID; oid UUID; pid UUID; pickid UUID; posid UUID; activevid UUID;
 key TEXT; stat TEXT; oldstat TEXT; oldtype TEXT; olddisposition TEXT; n INTEGER; eventcount INTEGER;
 ids UUID[]:='{}'; returnids UUID[]:='{}'; completed UUID[]:='{}'; compact JSONB; oldassembly JSONB;
BEGIN
 IF raw IS NULL OR raw='null'::jsonb THEN RETURN raw; END IF;
 -- Called inside the workspace transaction: this lock also protects migration/import.
 PERFORM 1 FROM assembly_preview_drafts WHERE id=true FOR UPDATE;
 FOR entry IN SELECT value FROM jsonb_array_elements(COALESCE(raw->'problemOrders','[]')) LOOP
  vid:=assembly_store_order(entry->'order'); SELECT order_id INTO oid FROM assembly_order_versions WHERE id=vid;
  problem:=entry->'problem'; key:=jsonb_build_array(oid,problem->>'type',problem->>'createdAt')::text;
  stat:=COALESCE(problem->>'resolutionStatus','awaiting');
  IF stat NOT IN ('awaiting','resolved','returned') THEN RAISE EXCEPTION 'Invalid problem status' USING ERRCODE='23514'; END IF;
  pid:=NULL;
  IF entry ? 'problemId' THEN
   SELECT id INTO pid FROM assembly_problem_records WHERE id=(entry->>'problemId')::uuid AND order_id=oid;
   IF pid IS NULL THEN RAISE EXCEPTION 'Unknown problem identity' USING ERRCODE='23514'; END IF;
  ELSE SELECT id INTO pid FROM assembly_problem_records WHERE legacy_key=key; END IF;
  IF pid IS NOT NULL THEN
   SELECT resolution_status,problem_type,assembly_metadata,disposition INTO oldstat,oldtype,oldassembly,olddisposition FROM assembly_problem_records WHERE id=pid;
   IF oldtype <> problem->>'type' THEN RAISE EXCEPTION 'Problem type cannot be overwritten' USING ERRCODE='23514'; END IF;
   IF stat='returned' AND oldstat <> 'resolved' THEN RAISE EXCEPTION 'Unresolved problem cannot be returned' USING ERRCODE='23514'; END IF;
   IF olddisposition='returned' AND stat <> 'returned' THEN RAISE EXCEPTION 'Closed problem cannot be reopened; create a new problem' USING ERRCODE='23514'; END IF;
   IF stat='returned' AND olddisposition <> 'returned' THEN returnids:=array_append(returnids,pid); END IF;
   IF (problem->>'createdAt')::timestamptz IS DISTINCT FROM (SELECT created_at FROM assembly_problem_records WHERE id=pid) THEN RAISE EXCEPTION 'Creation time is immutable' USING ERRCODE='23514'; END IF;
   IF (entry->'assembly')-'picks'-'cell' IS DISTINCT FROM oldassembly-'cell' THEN RAISE EXCEPTION 'Saved picking context cannot be overwritten' USING ERRCODE='23514'; END IF;
   IF entry->'assembly'->'picks' IS DISTINCT FROM assembly_load_problem(pid)->'assembly'->'picks' THEN RAISE EXCEPTION 'Saved picks cannot be overwritten' USING ERRCODE='23514'; END IF;
   UPDATE assembly_problem_records SET order_version_id=vid,resolution_status=CASE WHEN stat='returned' THEN 'resolved' ELSE stat END,
    disposition=CASE WHEN stat='returned' THEN 'returned' ELSE 'problem' END,
    problem_metadata=(problem-'shortages'-'history') || CASE WHEN problem ? 'history' THEN '{"history":true}'::jsonb ELSE '{}'::jsonb END,
    assembly_metadata=(entry->'assembly')-'picks',requires_disassembly=(problem->>'requiresDisassembly')::boolean WHERE id=pid;
  ELSE
   IF stat <> 'awaiting' AND NOT importing THEN RAISE EXCEPTION 'New problem must await resolution' USING ERRCODE='23514'; END IF;
   -- Legacy rows without history get a creation record, never invented scan timestamps.
   IF jsonb_array_length(COALESCE(problem->'history','[]'))=0 THEN
    problem:=problem || jsonb_build_object('history',jsonb_build_array(jsonb_build_object('createdAt',problem->>'createdAt','kind','creation','title','Проблема создана','message',problem->>'type','outcome','neutral')));
   END IF;
   INSERT INTO assembly_problem_records(order_id,order_version_id,creation_order_version_id,legacy_key,problem_type,resolution_status,disposition,created_at,creation_problem_metadata,problem_metadata,entry_metadata,assembly_metadata,requires_disassembly)
   VALUES(oid,vid,vid,key,problem->>'type',CASE WHEN stat='returned' THEN 'resolved' ELSE stat END,CASE WHEN stat='returned' THEN 'returned' ELSE 'problem' END,(problem->>'createdAt')::timestamptz,
    problem-'history',(problem-'shortages'-'history') || CASE WHEN problem ? 'history' THEN '{"history":true}'::jsonb ELSE '{}'::jsonb END,
    entry-'order'-'problem'-'assembly'-'problemId',(entry->'assembly')-'picks',(problem->>'requiresDisassembly')::boolean) RETURNING id INTO pid;
   n:=0;
   FOR pick IN SELECT value FROM jsonb_array_elements(COALESCE(entry->'assembly'->'picks','[]')) LOOP
    pickid:=assembly_store_pick(oid,pick,n,vid);
    INSERT INTO assembly_problem_pick_links(problem_id,ordinal,pick_id) VALUES(pid,n,pickid); n:=n+1;
   END LOOP;
   INSERT INTO assembly_problem_affected_positions(problem_id,order_position_id,sku,creation_payload)
   SELECT pid,id,sku,payload FROM assembly_order_positions p WHERE version_id=vid AND
    (sku=problem->>'positionSku' OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(problem->'shortages','[]')) s WHERE s->>'sku'=p.sku)
    OR (problem->>'positionSku' IS NULL AND jsonb_array_length(COALESCE(problem->'shortages','[]'))=0));
  END IF;
  IF pid=ANY(ids) THEN RAISE EXCEPTION 'Duplicate problem identity in request' USING ERRCODE='23514'; END IF;
  ids:=array_append(ids,pid);
  -- Mutable shortage observations are separate from immutable creation version and pick links.
  DELETE FROM assembly_problem_positions WHERE problem_id=pid;
  n:=0;
  FOR pos IN SELECT value FROM jsonb_array_elements(COALESCE(problem->'shortages','[]')) LOOP
   SELECT id INTO posid FROM assembly_order_positions WHERE version_id=vid AND sku=pos->>'sku';
   INSERT INTO assembly_problem_positions(problem_id,order_position_id,sku,ordinal,required_quantity,available_quantity,missing_quantity,is_shortage,payload)
   VALUES(pid,posid,pos->>'sku',n,(pos->>'required')::integer,(pos->>'available')::integer,(pos->>'missing')::integer,true,pos); n:=n+1;
  END LOOP;
  IF problem->>'positionSku' IS NOT NULL AND NOT EXISTS(SELECT 1 FROM assembly_problem_positions WHERE problem_id=pid AND sku=problem->>'positionSku') THEN
   SELECT id INTO posid FROM assembly_order_positions WHERE version_id=vid AND sku=problem->>'positionSku';
   INSERT INTO assembly_problem_positions(problem_id,order_position_id,sku,ordinal,is_shortage) VALUES(pid,posid,problem->>'positionSku',n,false);
  END IF;
  SELECT count(*) INTO eventcount FROM assembly_problem_events WHERE problem_id=pid;
  IF jsonb_array_length(COALESCE(problem->'history','[]')) < eventcount THEN RAISE EXCEPTION 'History cannot be removed' USING ERRCODE='23514'; END IF;
  n:=0;
  FOR event IN SELECT value FROM jsonb_array_elements(COALESCE(problem->'history','[]')) LOOP
   IF n < eventcount THEN
    SELECT payload INTO old_event FROM assembly_problem_events WHERE problem_id=pid AND ordinal=n;
    IF old_event <> event THEN RAISE EXCEPTION 'History cannot be rewritten' USING ERRCODE='23514'; END IF;
   ELSE INSERT INTO assembly_problem_events(problem_id,ordinal,kind,occurred_at,payload) VALUES(pid,n,COALESCE(event->>'kind','legacy'),(event->>'createdAt')::timestamptz,event); END IF;
   n:=n+1;
  END LOOP;
 END LOOP;
 IF EXISTS(SELECT 1 FROM assembly_problem_records WHERE NOT(id=ANY(ids))) THEN RAISE EXCEPTION 'Problems cannot be removed from storage' USING ERRCODE='23514'; END IF;
 activevid:=assembly_store_order(raw->'activeOrder');
 IF activevid IS NOT NULL THEN
  SELECT order_id INTO oid FROM assembly_order_versions WHERE id=activevid;
  IF EXISTS(SELECT 1 FROM assembly_problem_records WHERE order_id=oid AND (resolution_status='awaiting' OR problem_type='cancelled_order' OR requires_disassembly IS TRUE)) THEN
   RAISE EXCEPTION 'Order still has unresolved problems or is cancelled' USING ERRCODE='23514';
  END IF;
 END IF;
 IF EXISTS(SELECT 1 FROM assembly_problem_records p WHERE id=ANY(returnids) AND EXISTS(SELECT 1 FROM assembly_problem_records other WHERE other.order_id=p.order_id AND (other.resolution_status='awaiting' OR other.requires_disassembly IS TRUE OR other.problem_type='cancelled_order'))) THEN
  RAISE EXCEPTION 'All problems must be resolved before return' USING ERRCODE='23514';
 END IF;
 DELETE FROM assembly_workspace_pick_links; n:=0;
 FOR pick IN SELECT value FROM jsonb_array_elements(COALESCE(raw->'activeState'->'picks','[]')) LOOP
  IF activevid IS NULL THEN RAISE EXCEPTION 'Picks require active order' USING ERRCODE='23514'; END IF;
  INSERT INTO assembly_workspace_pick_links(ordinal,pick_id) VALUES(n,assembly_store_pick(oid,pick,n,activevid)); n:=n+1;
 END LOOP;
 FOR entry IN SELECT value FROM jsonb_array_elements(COALESCE(raw->'completedOrders','[]')) LOOP
  vid:=assembly_store_order(entry);
  IF EXISTS(SELECT 1 FROM assembly_problem_records p JOIN assembly_order_versions v ON v.order_id=p.order_id WHERE v.id=vid AND (p.resolution_status='awaiting' OR p.problem_type='cancelled_order' OR p.requires_disassembly IS TRUE)) THEN RAISE EXCEPTION 'Cannot complete blocked order' USING ERRCODE='23514'; END IF;
  completed:=array_append(completed,vid);
 END LOOP;
 UPDATE assembly_order_records o SET status='problem' WHERE EXISTS(SELECT 1 FROM assembly_problem_records p WHERE p.order_id=o.id AND p.disposition='problem');
 UPDATE assembly_order_records SET status='in_progress' WHERE id=(SELECT order_id FROM assembly_order_versions WHERE id=activevid);
 UPDATE assembly_order_records SET status='completed' WHERE id IN (SELECT order_id FROM assembly_order_versions WHERE id=ANY(completed));
 compact:=raw-'activeOrder'-'problemOrders'-'completedOrders';
 compact:=jsonb_set(compact,'{activeState}',(raw->'activeState')-'picks');
 RETURN compact || jsonb_build_object('storageVersion',2,'activeOrderVersionId',activevid,'completedOrderVersionIds',completed,'problemIds',ids);
END $$;

CREATE FUNCTION assembly_load_workspace(compact JSONB) RETURNS JSONB LANGUAGE plpgsql STABLE AS $$
DECLARE state JSONB; active JSONB; problems JSONB; completed JSONB; picks JSONB;
BEGIN
 IF compact IS NULL OR compact='null'::jsonb OR compact->>'storageVersion' IS DISTINCT FROM '2' THEN RETURN compact; END IF;
 active:=assembly_load_order((compact->>'activeOrderVersionId')::uuid);
 SELECT COALESCE(jsonb_agg(assembly_load_problem(value::uuid) ORDER BY ordinal),'[]'::jsonb) INTO problems
 FROM jsonb_array_elements_text(compact->'problemIds') WITH ORDINALITY x(value,ordinal);
 SELECT COALESCE(jsonb_agg(assembly_load_order(value::uuid) ORDER BY ordinal),'[]'::jsonb) INTO completed
 FROM jsonb_array_elements_text(compact->'completedOrderVersionIds') WITH ORDINALITY x(value,ordinal);
 SELECT COALESCE(jsonb_agg(jsonb_build_object('sku',p.position_sku,'cell',c.display_code) ORDER BY l.ordinal),'[]'::jsonb) INTO picks
 FROM assembly_workspace_pick_links l JOIN assembly_pick_records p ON p.id=l.pick_id JOIN assembly_cell_references c ON c.id=p.cell_reference_id;
 state:=compact-'storageVersion'-'activeOrderVersionId'-'completedOrderVersionIds'-'problemIds';
 state:=jsonb_set(state,'{activeState}',(state->'activeState') || jsonb_build_object('picks',picks));
 RETURN state || jsonb_build_object('activeOrder',active,'problemOrders',problems,'completedOrders',completed);
END $$;

UPDATE assembly_preview_drafts SET state=assembly_store_workspace(state,true) WHERE state IS NOT NULL;
COMMIT;
