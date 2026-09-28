CREATE OR REPLACE FUNCTION reject_provider_event_rewrite()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.provider <> OLD.provider OR NEW.event_id <> OLD.event_id
       OR NEW.event_type <> OLD.event_type OR NEW.payload <> OLD.payload
       OR NEW.provider_payment_id IS DISTINCT FROM OLD.provider_payment_id
       OR NEW.received_at <> OLD.received_at THEN
        RAISE EXCEPTION 'provider event % is evidence and cannot be rewritten', OLD.id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
ALTER TABLE provider_events DROP COLUMN IF EXISTS object_kind;
ALTER TABLE provider_events RENAME COLUMN object_id TO provider_payment_id;

DROP TABLE IF EXISTS refunds;
DROP FUNCTION IF EXISTS reject_final_refund_change();

ALTER TABLE payments DROP CONSTRAINT IF EXISTS ck_payments_refunded;
ALTER TABLE payments DROP COLUMN IF EXISTS refunded_amount;
ALTER TABLE payments DROP CONSTRAINT ck_payments_captured;
ALTER TABLE payments ADD CONSTRAINT ck_payments_captured CHECK (
    (status IN ('captured', 'settled')) = (captured_amount IS NOT NULL));
ALTER TABLE payments DROP CONSTRAINT payments_status_check;
ALTER TABLE payments ADD CONSTRAINT payments_status_check CHECK (status IN (
    'created', 'pending', 'authorized', 'captured', 'settled', 'failed', 'expired', 'cancelled'));
