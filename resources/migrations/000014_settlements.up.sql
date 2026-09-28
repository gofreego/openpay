-- Migration: 000014_settlements
-- What the PSP says it paid us, matched item by item against what our ledger
-- says happened. Until this exists, the ledger is trusted but unverified.

CREATE TABLE settlements (
    id                     BIGSERIAL   PRIMARY KEY,
    public_id              TEXT        NOT NULL,
    provider               TEXT        NOT NULL,
    provider_settlement_id TEXT        NOT NULL,
    -- The bank account it landed in, and the bank's reference for the credit.
    bank                   TEXT        NOT NULL,
    bank_reference         TEXT        NOT NULL DEFAULT '',
    settled_at             TIMESTAMPTZ NOT NULL,

    -- Totals as the provider reported them. net is what reached the bank.
    gross      BIGINT NOT NULL,
    fees       BIGINT NOT NULL,
    fee_tax    BIGINT NOT NULL,
    net        BIGINT NOT NULL,
    currency   CHAR(3) NOT NULL,
    item_count INTEGER NOT NULL,

    -- The report itself, verbatim: the evidence every match is judged against.
    raw        BYTEA  NOT NULL,

    -- clean: every item matched. breaks: at least one did not.
    status     TEXT   NOT NULL CHECK (status IN ('clean', 'breaks')),

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_settlements_public_id    UNIQUE (public_id),
    -- Ingesting the same report twice books the bank credit once.
    CONSTRAINT uq_settlements_provider_ref UNIQUE (provider, provider_settlement_id)
);

CREATE TABLE settlement_items (
    id            BIGSERIAL   PRIMARY KEY,
    settlement_id BIGINT      NOT NULL REFERENCES settlements (id),

    -- payment, refund or chargeback, with the provider's id for it.
    kind          TEXT        NOT NULL CHECK (kind IN ('payment', 'refund', 'chargeback')),
    provider_ref  TEXT        NOT NULL,

    -- Signed as the provider reports them: money in positive, refunds and
    -- chargebacks negative. fee and fee_tax are what the provider charged.
    gross         BIGINT      NOT NULL,
    fee           BIGINT      NOT NULL,
    fee_tax       BIGINT      NOT NULL,
    net           BIGINT      NOT NULL,

    -- What our ledger expected to settle for this item, and what did not
    -- match it (net + fee + fee_tax - expected), parked in suspense.
    expected      BIGINT      NOT NULL,
    unexplained   BIGINT      NOT NULL,

    -- The ledger record it matched, and the product carried through from it
    -- — the per-product split of one mixed bank credit depends on this.
    payment_id    BIGINT      REFERENCES payments (id),
    refund_id     BIGINT      REFERENCES refunds (id),
    dispute_id    BIGINT      REFERENCES disputes (id),
    product_id    BIGINT      REFERENCES products (id),

    -- matched, or the kind of break.
    classification TEXT       NOT NULL CHECK (classification IN (
        'matched', 'missing_in_ledger', 'amount_mismatch', 'fee_mismatch', 'duplicate')),

    CONSTRAINT ck_settlement_items_unexplained CHECK (unexplained = net + fee + fee_tax - expected)
);

CREATE INDEX idx_settlement_items_settlement ON settlement_items (settlement_id);
CREATE INDEX idx_settlement_items_payment    ON settlement_items (payment_id) WHERE payment_id IS NOT NULL;
-- One provider reference settles once. A second appearance is classified a
-- duplicate before insert; this index makes sure no path forgets to.
CREATE UNIQUE INDEX uq_settlement_items_matched
    ON settlement_items (kind, provider_ref) WHERE classification = 'matched';

-- The ops work queue: every break, from a settlement item or from a captured
-- payment the provider never settled.
CREATE TABLE recon_breaks (
    id                 BIGSERIAL   PRIMARY KEY,
    public_id          TEXT        NOT NULL,
    provider           TEXT        NOT NULL,
    classification     TEXT        NOT NULL CHECK (classification IN (
        'missing_in_ledger', 'missing_at_provider', 'amount_mismatch', 'fee_mismatch', 'duplicate')),
    settlement_item_id BIGINT      REFERENCES settlement_items (id),
    payment_id         BIGINT      REFERENCES payments (id),
    product_id         BIGINT      REFERENCES products (id),
    -- The amount in question: what sits in suspense for an item break, what
    -- sits unsettled in the receivable for a missing-at-provider one.
    amount             BIGINT      NOT NULL,
    currency           CHAR(3)     NOT NULL,
    detail             TEXT        NOT NULL,

    status             TEXT        NOT NULL CHECK (status IN ('open', 'resolved', 'force_matched', 'written_off')),
    reason_code        TEXT,
    note               TEXT,
    resolved_by        TEXT,
    resolved_at        TIMESTAMPTZ,

    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_recon_breaks_public_id UNIQUE (public_id),
    CONSTRAINT ck_recon_breaks_resolution CHECK ((status = 'open') = (resolved_at IS NULL))
);

CREATE INDEX idx_recon_breaks_open ON recon_breaks (created_at) WHERE status = 'open';
-- A payment is flagged missing at its provider once, not every day it stays missing.
CREATE UNIQUE INDEX uq_recon_breaks_missing_at_provider
    ON recon_breaks (payment_id) WHERE classification = 'missing_at_provider' AND status = 'open';

CREATE TRIGGER trg_recon_breaks_updated_at
BEFORE UPDATE ON recon_breaks
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE payments ADD COLUMN settled_at TIMESTAMPTZ;
