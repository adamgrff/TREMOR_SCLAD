BEGIN;

-- No historical start/finish timestamps are inferred or backfilled.
CREATE TABLE assembly_lifecycle_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id UUID NOT NULL REFERENCES assembly_order_records(id) ON DELETE RESTRICT,
    cycle_id UUID NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('started', 'completed')),
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    request_id UUID,
    operation_id UUID UNIQUE,
    UNIQUE (order_id, cycle_id, event_type),
    UNIQUE (request_id, event_type),
    CHECK ((event_type = 'started' AND request_id IS NOT NULL AND cycle_id = request_id AND operation_id IS NULL)
        OR (event_type = 'completed' AND request_id IS NULL AND operation_id IS NOT NULL))
);

CREATE INDEX assembly_lifecycle_completed_time_idx ON assembly_lifecycle_events(occurred_at, order_id)
    WHERE event_type = 'completed';

CREATE FUNCTION assembly_lifecycle_validate_order() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM assembly_order_records WHERE id = NEW.order_id AND source = 'ozon') THEN
        RAISE EXCEPTION 'Lifecycle events require a real Ozon order' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION assembly_lifecycle_events_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'Assembly lifecycle events are immutable' USING ERRCODE = '23514';
END;
$$;

CREATE TRIGGER assembly_lifecycle_real_order BEFORE INSERT ON assembly_lifecycle_events
    FOR EACH ROW EXECUTE FUNCTION assembly_lifecycle_validate_order();
CREATE TRIGGER assembly_lifecycle_no_rewrite BEFORE UPDATE OR DELETE ON assembly_lifecycle_events
    FOR EACH ROW EXECUTE FUNCTION assembly_lifecycle_events_immutable();
CREATE TRIGGER assembly_lifecycle_no_truncate BEFORE TRUNCATE ON assembly_lifecycle_events
    FOR EACH STATEMENT EXECUTE FUNCTION assembly_lifecycle_events_immutable();

COMMIT;
