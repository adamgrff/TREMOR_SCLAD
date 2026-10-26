BEGIN;

-- Persists the current assembly preview only. Does not change warehouse stock.
CREATE TABLE assembly_preview_drafts (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    version BIGINT NOT NULL DEFAULT 0 CHECK (version >= 0),
    state JSONB,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (state IS NULL OR jsonb_typeof(state) = 'object')
);

CREATE TABLE assembly_preview_requests (
    request_id UUID PRIMARY KEY,
    payload_hash TEXT NOT NULL,
    response JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMIT;
