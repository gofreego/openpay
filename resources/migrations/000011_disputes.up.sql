-- Migration: 000011_disputes
-- Chargebacks: a customer asks their bank to take a payment back.

ALTER TABLE payments DROP CONSTRAINT payments_status_check;
ALTER TABLE payments ADD CONSTRAINT payments_status_check CHECK (status IN (
    'created', 'pending', 'authorized', 'captured', 'settled',
    'partially_refunded', 'refunded',
    'disputed', 'dispute_won', 'dispute_lost',
    'failed', 'expired', 'cancelled'));

ALTER TABLE payments DROP CONSTRAINT ck_payments_captured;
ALTER TABLE payments ADD CONSTRAINT ck_payments_captured CHECK (
    (status IN ('captured', 'settled', 'partially_refunded', 'refunded',
                'disputed', 'dispute_won', 'dispute_lost')) = (captured_amount IS NOT NULL)
);

CREATE TABLE disputes (
    id          BIGSERIAL   PRIMARY KEY,
    public_id   TEXT        NOT NULL,
    payment_id  BIGINT      NOT NULL REFERENCES payments (id),
    product_id  BIGINT      NOT NULL REFERENCES products (id),

    provider            TEXT NOT NULL,
    provider_dispute_id TEXT NOT NULL,

    -- The provider's figure, never ours (plan.md D7).
    amount      BIGINT      NOT NULL CHECK (amount > 0),
    currency    CHAR(3)     NOT NULL,
    -- The card network's reason, verbatim: fraud, product_not_received, …
    reason      TEXT        NOT NULL DEFAULT '',

    status      TEXT        NOT NULL CHECK (status IN ('open', 'under_review', 'won', 'lost')),

    -- Where the contested money was taken from when the dispute opened, so a
    -- win can put each part back exactly where it came from. They sum to amount.
    from_wallet    BIGINT   NOT NULL DEFAULT 0 CHECK (from_wallet >= 0),
    from_unapplied BIGINT   NOT NULL DEFAULT 0 CHECK (from_unapplied >= 0),
    -- The part the customer had already spent: our loss unless we win.
    from_expense   BIGINT   NOT NULL DEFAULT 0 CHECK (from_expense >= 0),

    evidence_due_by       TIMESTAMPTZ,
    evidence              TEXT,
    evidence_submitted_by TEXT,
    evidence_submitted_at TIMESTAMPTZ,
    resolved_at           TIMESTAMPTZ,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_disputes_public_id    UNIQUE (public_id),
    CONSTRAINT uq_disputes_provider_ref UNIQUE (provider, provider_dispute_id),
    CONSTRAINT ck_disputes_sources CHECK (from_wallet + from_unapplied + from_expense = amount)
);

CREATE INDEX idx_disputes_payment ON disputes (payment_id);
CREATE INDEX idx_disputes_open    ON disputes (updated_at) WHERE status IN ('open', 'under_review');

CREATE TRIGGER trg_disputes_updated_at
BEFORE UPDATE ON disputes
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE OR REPLACE FUNCTION reject_resolved_dispute_change()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status IN ('won', 'lost') THEN
        RAISE EXCEPTION 'dispute % is already %; a resolved dispute cannot change', OLD.public_id, OLD.status;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_disputes_resolved
BEFORE UPDATE ON disputes
FOR EACH ROW EXECUTE FUNCTION reject_resolved_dispute_change();
