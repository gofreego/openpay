DROP INDEX IF EXISTS idx_withdrawals_customer_day;
ALTER TABLE wallet_types DROP CONSTRAINT IF EXISTS ck_wallet_types_daily_withdrawal,
    DROP COLUMN IF EXISTS daily_withdrawal_limit;
