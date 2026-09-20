-- Migration: 000002_idempotency_and_outbox
-- The two mechanisms every mutating endpoint depends on: retry safety for
-- callers, and reliable event publishing for us.

-- idempotency_keys makes a retried request safe to send. The UNIQUE constraint
-- is the actual guarantee; the surrounding code is only bookkeeping.
--
-- scope namespaces the key so two callers can independently use "order-42".
-- It holds a product id from phase 1 onward.
CREATE TABLE idempotency_keys (
    id                  BIGSERIAL PRIMARY KEY,
    scope               TEXT        NOT NULL,
    idempotency_key     TEXT        NOT NULL,

    -- Hash of method plus request body. A key reused with different content is
    -- a caller bug, and must be rejected rather than served a stale response.
    request_fingerprint TEXT        NOT NULL,

    -- in_progress: claimed, work running or abandoned.
    -- completed:   response_body is the recorded result, safe to replay.
    status              TEXT        NOT NULL CHECK (status IN ('in_progress', 'completed')),
    response_body       BYTEA,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at          TIMESTAMPTZ NOT NULL,

    CONSTRAINT uq_idempotency_scope_key UNIQUE (scope, idempotency_key),
    CONSTRAINT ck_idempotency_completed_has_body
        CHECK (status <> 'completed' OR response_body IS NOT NULL)
);

CREATE INDEX idx_idempotency_expires_at ON idempotency_keys (expires_at);

CREATE TRIGGER trg_idempotency_keys_updated_at
BEFORE UPDATE ON idempotency_keys
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- outbox_events is written in the same transaction as the work it describes,
-- so an event cannot exist without its cause and vice versa. A worker publishes
-- them afterwards. This is what replaces "write the row, then call Kafka",
-- which loses events whenever the second step fails.
CREATE TABLE outbox_events (
    id             BIGSERIAL PRIMARY KEY,
    event_id       TEXT        NOT NULL,
    topic          TEXT        NOT NULL,
    aggregate_type TEXT        NOT NULL,
    aggregate_id   TEXT        NOT NULL,
    payload        BYTEA       NOT NULL,

    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at   TIMESTAMPTZ,
    attempts       INTEGER     NOT NULL DEFAULT 0,
    last_error     TEXT,

    -- Consumers dedupe on event_id, so it must be unique at the source.
    CONSTRAINT uq_outbox_event_id UNIQUE (event_id)
);

-- Partial index: the table grows forever, but the drainer only ever looks at
-- the unpublished tail, which stays small.
CREATE INDEX idx_outbox_unpublished ON outbox_events (id) WHERE published_at IS NULL;
