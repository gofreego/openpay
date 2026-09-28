DROP TABLE IF EXISTS disputes;
DROP FUNCTION IF EXISTS reject_resolved_dispute_change();
ALTER TABLE payments DROP CONSTRAINT ck_payments_captured;
ALTER TABLE payments ADD CONSTRAINT ck_payments_captured CHECK (
    (status IN ('captured', 'settled', 'partially_refunded', 'refunded')) = (captured_amount IS NOT NULL));
ALTER TABLE payments DROP CONSTRAINT payments_status_check;
ALTER TABLE payments ADD CONSTRAINT payments_status_check CHECK (status IN (
    'created', 'pending', 'authorized', 'captured', 'settled',
    'partially_refunded', 'refunded', 'failed', 'expired', 'cancelled'));
