ALTER TABLE wallet_types
    DROP CONSTRAINT IF EXISTS ck_wallet_types_withdrawal_policy_needs_withdrawable,
    DROP CONSTRAINT IF EXISTS ck_wallet_types_withdrawal_policy,
    DROP COLUMN IF EXISTS withdrawal_approval_threshold,
    DROP COLUMN IF EXISTS min_withdrawal_amount;
