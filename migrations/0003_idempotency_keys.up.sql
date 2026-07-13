-- Idempotency keys let a client safely retry a charge without risking a double
-- charge. The client sends a unique Idempotency-Key header; the server remembers
-- what it did the first time and replays that result on any retry.
--
-- The primary key on `key` is what makes the whole thing work: an
-- `INSERT ... ON CONFLICT DO NOTHING` against this unique key is an atomic
-- test-and-set — exactly one of many racing identical requests can insert the
-- row, and that one becomes the winner.
CREATE TABLE idempotency_keys (
    key            TEXT PRIMARY KEY,
    request_hash   TEXT NOT NULL,                       -- SHA-256 of the request body
    status         TEXT NOT NULL CHECK (status IN ('in_progress', 'completed')),
    response_code  INT,                                 -- filled in once the request completes
    -- Stored as raw bytes (not JSONB) so a retry replays the EXACT original
    -- response. JSONB would re-serialize it (reorder keys, change whitespace),
    -- changing the bytes the client sees on a retry vs. the first call.
    response_body  BYTEA,
    transaction_id BIGINT REFERENCES transactions(id),  -- the charge this key produced, if any
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL                 -- keys are reclaimable after this
);

-- Supports a future cleanup job that deletes expired keys.
CREATE INDEX idx_idempotency_keys_expires_at ON idempotency_keys (expires_at);
