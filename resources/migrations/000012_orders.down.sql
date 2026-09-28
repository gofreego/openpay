ALTER TABLE payments DROP CONSTRAINT IF EXISTS ck_payments_target;
DROP INDEX IF EXISTS idx_payments_order;
ALTER TABLE payments DROP COLUMN IF EXISTS order_id;
ALTER TABLE payments ADD CONSTRAINT ck_payments_topup_target CHECK (
    purpose <> 'wallet_topup' OR (wallet_id IS NOT NULL AND customer_id IS NOT NULL));
DROP TABLE IF EXISTS order_tenders;
DROP TABLE IF EXISTS order_line_items;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS items;
