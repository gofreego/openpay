-- Rollback: 000002_idempotency_and_outbox

DROP TABLE IF EXISTS outbox_events;

DROP TRIGGER IF EXISTS trg_idempotency_keys_updated_at ON idempotency_keys;
DROP TABLE IF EXISTS idempotency_keys;
