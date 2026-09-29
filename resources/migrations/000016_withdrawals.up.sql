-- Migration: 000016_withdrawals
-- Cashing a withdrawable wallet out to a customer's bank account or UPI id.

-- Where a customer's withdrawals may go. Verified with the provider (a
-- penny-drop, or a VPA lookup) before any money is sent.
CREATE TABLE beneficiaries (
    id             BIGSERIAL   PRIMARY KEY,
    public_id      TEXT        NOT NULL,
    customer_id    BIGINT      NOT NULL REFERENCES customers (id),
    kind           TEXT        NOT NULL CHECK (kind IN ('bank_account', 'vpa')),
    name           TEXT        NOT NULL,
    -- Stored in full because a payout needs it; the API only ever returns it
    -- masked.
    account_number TEXT,
    ifsc           TEXT,
    vpa            TEXT,

    status         TEXT        NOT NULL CHECK (status IN ('pending', 'verified', 'failed')),
    -- The name the bank or UPI directory reports: what a person checks
    -- against the name the customer typed.
    name_at_bank   TEXT,
    verified_at    TIMESTAMPTZ,
    failure_reason TEXT,

    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_beneficiaries_public_id UNIQUE (public_id),
    CONSTRAINT ck_beneficiaries_details CHECK (
        (kind = 'bank_account' AND account_number IS NOT NULL AND ifsc IS NOT NULL AND vpa IS NULL) OR
        (kind = 'vpa' AND vpa IS NOT NULL AND account_number IS NULL AND ifsc IS NULL)),
    CONSTRAINT ck_beneficiaries_verified CHECK ((status = 'verified') = (verified_at IS NOT NULL))
);

CREATE INDEX idx_beneficiaries_customer ON beneficiaries (customer_id);
-- The same destination is registered once per customer.
CREATE UNIQUE INDEX uq_beneficiaries_destination
    ON beneficiaries (customer_id, kind, COALESCE(account_number, ''), COALESCE(ifsc, ''), COALESCE(vpa, ''));

CREATE TRIGGER trg_beneficiaries_updated_at
BEFORE UPDATE ON beneficiaries
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE withdrawals (
    id             BIGSERIAL   PRIMARY KEY,
    public_id      TEXT        NOT NULL,
    wallet_id      BIGINT      NOT NULL REFERENCES wallets (id),
    customer_id    BIGINT      NOT NULL REFERENCES customers (id),
    product_id     BIGINT      REFERENCES products (id),
    beneficiary_id BIGINT      NOT NULL REFERENCES beneficiaries (id),

    amount         BIGINT      NOT NULL CHECK (amount > 0),
    currency       CHAR(3)     NOT NULL,

    -- pending_approval: above the type's threshold, waiting for a person.
    -- approved: cleared to send; the provider has not yet confirmed it has it.
    -- processing: with the provider. paid: confirmed at the bank.
    -- failed / rejected: never left; the money went back to the wallet.
    -- reversed: left, then bounced; the money went back to the wallet.
    status         TEXT        NOT NULL CHECK (status IN (
        'pending_approval', 'approved', 'processing', 'paid', 'failed', 'rejected', 'reversed')),
    requires_approval BOOLEAN  NOT NULL,

    requested_by   TEXT        NOT NULL,
    decided_by     TEXT,
    decided_at     TIMESTAMPTZ,
    decision_note  TEXT,

    provider           TEXT    NOT NULL,
    provider_payout_id TEXT,
    failure_reason     TEXT,
    paid_at            TIMESTAMPTZ,

    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_withdrawals_public_id UNIQUE (public_id),
    -- Maker-checker: whoever asked for a withdrawal cannot approve it.
    CONSTRAINT ck_withdrawals_four_eyes CHECK (decided_by IS NULL OR decided_by <> requested_by)
);

CREATE UNIQUE INDEX uq_withdrawals_provider_ref ON withdrawals (provider, provider_payout_id)
    WHERE provider_payout_id IS NOT NULL;
CREATE INDEX idx_withdrawals_wallet ON withdrawals (wallet_id, id DESC);
CREATE INDEX idx_withdrawals_queue  ON withdrawals (created_at) WHERE status = 'pending_approval';
CREATE INDEX idx_withdrawals_open   ON withdrawals (updated_at) WHERE status IN ('approved', 'processing');

CREATE TRIGGER trg_withdrawals_updated_at
BEFORE UPDATE ON withdrawals
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Payout webhooks carry their own object.
ALTER TABLE provider_events DROP CONSTRAINT provider_events_object_kind_check;
ALTER TABLE provider_events ADD CONSTRAINT provider_events_object_kind_check
    CHECK (object_kind IN ('payment', 'refund', 'dispute', 'payout'));
