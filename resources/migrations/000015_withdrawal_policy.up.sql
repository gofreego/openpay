-- Migration: 000015_withdrawal_policy
-- How a withdrawable wallet type may be cashed out: the smallest withdrawal
-- accepted, and the size above which a person must approve it.

ALTER TABLE wallet_types
    ADD COLUMN min_withdrawal_amount         BIGINT,
    ADD COLUMN withdrawal_approval_threshold BIGINT,
    ADD CONSTRAINT ck_wallet_types_withdrawal_policy CHECK (
        (min_withdrawal_amount IS NULL OR min_withdrawal_amount > 0) AND
        (withdrawal_approval_threshold IS NULL OR withdrawal_approval_threshold > 0)
    ),
    -- A withdrawal policy on a type that cannot be withdrawn is a
    -- misconfiguration waiting to be mistaken for permission.
    ADD CONSTRAINT ck_wallet_types_withdrawal_policy_needs_withdrawable CHECK (
        withdrawable OR (min_withdrawal_amount IS NULL AND withdrawal_approval_threshold IS NULL)
    );
