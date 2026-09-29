-- Migration: 000017_withdrawal_limits
-- How much a wallet of a withdrawable type may cash out per day.
ALTER TABLE wallet_types
    ADD COLUMN daily_withdrawal_limit BIGINT,
    ADD CONSTRAINT ck_wallet_types_daily_withdrawal CHECK (
        (daily_withdrawal_limit IS NULL OR daily_withdrawal_limit > 0) AND
        (withdrawable OR daily_withdrawal_limit IS NULL));

-- Serves the daily totals, per wallet and per customer.
CREATE INDEX idx_withdrawals_customer_day ON withdrawals (customer_id, created_at);
