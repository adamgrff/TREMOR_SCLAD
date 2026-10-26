BEGIN;
-- Persistent external identity -> warehouse ID. Neither old positions nor picks are rewritten.
CREATE TABLE assembly_sku_mappings (
 external_key TEXT PRIMARY KEY CHECK(length(external_key)>5),
 product_id BIGINT NOT NULL REFERENCES products(id),
 problem_id UUID NOT NULL REFERENCES assembly_problem_records(id),
 request_id UUID NOT NULL UNIQUE,
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER assembly_mappings_no_rewrite BEFORE UPDATE OR DELETE ON assembly_sku_mappings
 FOR EACH ROW EXECUTE FUNCTION assembly_events_immutable();
CREATE FUNCTION assembly_external_key(item JSONB) RETURNS TEXT LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE WHEN NULLIF(btrim(item->>'ozonSku'),'') IS NOT NULL
 THEN 'ozon:'||btrim(item->>'ozonSku') ELSE 'offer:'||upper(btrim(item->>'sku')) END
$$;
CREATE FUNCTION assembly_resolve_product(item JSONB, origin TEXT) RETURNS BIGINT LANGUAGE sql STABLE AS $$
 SELECT COALESCE(CASE WHEN origin='ozon' THEN
 (SELECT product_id FROM assembly_sku_mappings WHERE external_key=assembly_external_key(item)) END,
 (SELECT id FROM products WHERE upper(sku)=upper(btrim(item->>'sku'))))
$$;
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
  VALUES(vid,n,item->>'sku',(item->>'quantity')::integer,
   (SELECT id FROM products WHERE id=assembly_resolve_product(item,origin) AND NOT archived),item);
  n:=n+1;
 END LOOP;
 RETURN vid;
END $$;
COMMIT;
