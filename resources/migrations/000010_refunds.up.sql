-- Migration: 000010_refunds
-- Returning captured money to where it came from.

-- A payment can now be partly or wholly refunded (plan.md D6).
ALTER TABLE payments DROP CONSTRAINT payments_status_check;
ALTER TABLE payments ADD CONSTRAINT payments_status_check CHECK (status IN (
    'created', 'pending', 'authorized', 'captured', 'settled',
    'partially_refunded', 'refunded',
    'failed', 'expired', 'cancelled'));

ALTER TABLE payments DROP CONSTRAINT ck_payments_captured;
ALTER TABLE payments ADD CONSTRAINT ck_payments_captured CHECK (
    (status IN ('captured', 'settled', 'partially_refunded', 'refunded')) = (captured_amount IS NOT NULL)
);

-- Everything promised back so far: refunds initiated, pending or processed,
-- but not failed ones. Reserved when a refund starts, released if it fails.
--
-- This constraint is the over-refund protection. Two operators refunding the
-- same payment at once both pass any check made in code before either
-- commits; they cannot both pass this.
ALTER TABLE payments ADD COLUMN refunded_amount BIGINT NOT NULL DEFAULT 0;
ALTER TABLE payments ADD CONSTRAINT ck_payments_refunded CHECK (
    refunded_amount >= 0 AND refunded_amount <= COALESCE(captured_amount, 0)
);

CREATE TABLE refunds (
    id          BIGSERIAL   PRIMARY KEY,
    public_id   TEXT        NOT NULL,
    payment_id  BIGINT      NOT NULL REFERENCES payments (id),
    product_id  BIGINT      NOT NULL REFERENCES products (id),

    amount      BIGINT      NOT NULL CHECK (amount > 0),
    currency    CHAR(3)     NOT NULL,

    -- initiated: money reserved, provider not yet confirmed it has the
    -- request (a timeout leaves it here; the poller asks again). pending: the
    -- provider accepted it. processed and failed are final.
    status      TEXT        NOT NULL CHECK (status IN ('initiated', 'pending', 'processed', 'failed')),

    -- Where the money was taken from: the customer's wallet, or the
    -- refunds_payable balance of a payment whose wallet refused it.
    source      TEXT        NOT NULL CHECK (source IN ('wallet', 'unapplied')),

    reason_code TEXT        NOT NULL,
    memo        TEXT        NOT NULL DEFAULT '',

    provider           TEXT NOT NULL,
    provider_refund_id TEXT,
    failure_code       TEXT,
    failure_reason     TEXT,

    requested_by TEXT       NOT NULL,
    processed_at TIMESTAMPTZ,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_refunds_public_id UNIQUE (public_id)
);

CREATE UNIQUE INDEX uq_refunds_provider_ref ON refunds (provider, provider_refund_id)
    WHERE provider_refund_id IS NOT NULL;
CREATE INDEX idx_refunds_payment ON refunds (payment_id, id);
CREATE INDEX idx_refunds_open    ON refunds (updated_at) WHERE status IN ('initiated', 'pending');

CREATE TRIGGER trg_refunds_updated_at
BEFORE UPDATE ON refunds
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A final refund is history: reopening one could return money twice.
CREATE OR REPLACE FUNCTION reject_final_refund_change()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status IN ('processed', 'failed') THEN
        RAISE EXCEPTION 'refund % is already %; a final refund cannot change', OLD.public_id, OLD.status;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_refunds_final
BEFORE UPDATE ON refunds
FOR EACH ROW EXECUTE FUNCTION reject_final_refund_change();

-- Webhooks are no longer only about payments: a refund or a dispute has its
-- own provider id. The column is generalised rather than joined by siblings.
ALTER TABLE provider_events RENAME COLUMN provider_payment_id TO object_id;
ALTER TABLE provider_events ADD COLUMN object_kind TEXT NOT NULL DEFAULT 'payment'
    CHECK (object_kind IN ('payment', 'refund', 'dispute'));

CREATE OR REPLACE FUNCTION reject_provider_event_rewrite()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.provider <> OLD.provider OR NEW.event_id <> OLD.event_id
       OR NEW.event_type <> OLD.event_type OR NEW.payload <> OLD.payload
       OR NEW.object_kind <> OLD.object_kind
       OR NEW.object_id IS DISTINCT FROM OLD.object_id
       OR NEW.received_at <> OLD.received_at THEN
        RAISE EXCEPTION 'provider event % is evidence and cannot be rewritten', OLD.id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
