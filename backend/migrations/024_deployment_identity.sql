BEGIN;

CREATE TABLE deployment_identity (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    deployment_id UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

-- Provisioning only: GET/API startup must never create or rotate an identity.
INSERT INTO deployment_identity(singleton) VALUES (true);

CREATE FUNCTION deployment_identity_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'Deployment identity is immutable; independent clones require an explicit offline provisioning procedure' USING ERRCODE = '23514';
END;
$$;

CREATE TRIGGER deployment_identity_no_rewrite
BEFORE UPDATE OR DELETE ON deployment_identity
FOR EACH ROW EXECUTE FUNCTION deployment_identity_immutable();

CREATE TRIGGER deployment_identity_no_truncate
BEFORE TRUNCATE ON deployment_identity
FOR EACH STATEMENT EXECUTE FUNCTION deployment_identity_immutable();

COMMIT;
