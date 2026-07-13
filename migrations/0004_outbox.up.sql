-- The transactional outbox. When a charge commits, an event row is written in
-- the SAME database transaction as the charge (see internal/charges). Because
-- they commit together, it is impossible to end up with a charge that has no
-- event, or an event with no charge. A separate worker delivers these events.
CREATE TABLE outbox (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_type      TEXT NOT NULL,
    payload         BYTEA NOT NULL,             -- exact bytes to deliver (and sign)
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'delivered', 'dead')),
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The delivery worker polls for due, still-pending events; a partial index keeps
-- that query cheap as delivered events pile up.
CREATE INDEX idx_outbox_due ON outbox (next_attempt_at) WHERE status = 'pending';
