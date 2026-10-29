BEGIN;

-- No backfill: current pending/placed stock cannot prove historical intake.
CREATE TABLE receiving_intake_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id BIGINT NOT NULL REFERENCES receiving_sessions(id) ON DELETE RESTRICT,
    product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    quantity BIGINT NOT NULL CHECK (quantity > 0),
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX receiving_intake_events_time_idx ON receiving_intake_events(occurred_at);
CREATE INDEX receiving_intake_events_session_idx ON receiving_intake_events(session_id);
CREATE INDEX receiving_intake_events_product_idx ON receiving_intake_events(product_id);

CREATE FUNCTION receiving_intake_events_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'Receiving intake events are immutable' USING ERRCODE = '23514';
END;
$$;

CREATE TRIGGER receiving_intake_events_no_rewrite
BEFORE UPDATE OR DELETE ON receiving_intake_events
FOR EACH ROW EXECUTE FUNCTION receiving_intake_events_immutable();

CREATE TRIGGER receiving_intake_events_no_truncate
BEFORE TRUNCATE ON receiving_intake_events
FOR EACH STATEMENT EXECUTE FUNCTION receiving_intake_events_immutable();

COMMIT;
