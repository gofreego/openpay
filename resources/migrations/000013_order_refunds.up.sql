-- Migration: 000013_order_refunds
-- Returning an order's money, component by component, to where it came from.

-- Where an order refund goes by default (plan.md phase 6): back to how it was
-- paid, or as credit to the customer's wallet.
ALTER TABLE products ADD COLUMN refund_destination TEXT NOT NULL DEFAULT 'source'
    CHECK (refund_destination IN ('source', 'wallet'));

ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK (status IN (
    'pending_payment', 'paid', 'partially_refunded', 'refunded', 'failed', 'cancelled'));
ALTER TABLE orders DROP CONSTRAINT ck_orders_paid;
ALTER TABLE orders ADD CONSTRAINT ck_orders_paid CHECK (
    (status IN ('paid', 'partially_refunded', 'refunded')) = (paid_at IS NOT NULL));

-- What has been refunded so far, per component. The constraints are the
-- over-refund protection: concurrent refunds each pass any check in code
-- before either commits; they cannot both pass these.
ALTER TABLE orders
    ADD COLUMN refunded_subtotal BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN refunded_discount BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN refunded_tax      BIGINT NOT NULL DEFAULT 0,
    ADD CONSTRAINT ck_orders_refunded CHECK (
        refunded_subtotal BETWEEN 0 AND subtotal AND
        refunded_discount BETWEEN 0 AND discount AND
        refunded_tax      BETWEEN 0 AND tax AND
        refunded_subtotal - refunded_discount + refunded_tax BETWEEN 0 AND total);

ALTER TABLE order_tenders ADD COLUMN refunded_amount BIGINT NOT NULL DEFAULT 0;
ALTER TABLE order_tenders ADD CONSTRAINT ck_order_tenders_refunded CHECK (refunded_amount BETWEEN 0 AND amount);

CREATE TABLE order_refunds (
    id          BIGSERIAL   PRIMARY KEY,
    public_id   TEXT        NOT NULL,
    order_id    BIGINT      NOT NULL REFERENCES orders (id),
    product_id  BIGINT      NOT NULL REFERENCES products (id),

    -- The refund's own breakdown (plan.md D13). A partial refund's tax cannot
    -- be derived legitimately, so the product sends it; when it does not, a
    -- proportional split is used and this says so.
    amount      BIGINT      NOT NULL CHECK (amount > 0),
    subtotal    BIGINT      NOT NULL CHECK (subtotal >= 0),
    discount    BIGINT      NOT NULL CHECK (discount >= 0),
    tax         BIGINT      NOT NULL CHECK (tax >= 0),
    tax_breakdown_provided BOOLEAN NOT NULL,

    destination TEXT        NOT NULL CHECK (destination IN ('source', 'wallet')),
    reason_code TEXT        NOT NULL,
    memo        TEXT        NOT NULL DEFAULT '',
    requested_by TEXT       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_order_refunds_public_id UNIQUE (public_id),
    CONSTRAINT ck_order_refunds_arithmetic CHECK (subtotal - discount + tax = amount)
);

CREATE INDEX idx_order_refunds_order ON order_refunds (order_id);

-- How each refund was split across the tenders that paid the order.
CREATE TABLE order_refund_parts (
    id              BIGSERIAL PRIMARY KEY,
    order_refund_id BIGINT    NOT NULL REFERENCES order_refunds (id),
    tender_id       BIGINT    NOT NULL REFERENCES order_tenders (id),
    amount          BIGINT    NOT NULL CHECK (amount > 0),
    -- Where this part went: a wallet credit, or a card refund.
    wallet_id       BIGINT    REFERENCES wallets (id),
    refund_id       BIGINT    REFERENCES refunds (id),
    CONSTRAINT ck_order_refund_parts_target CHECK ((wallet_id IS NULL) <> (refund_id IS NULL))
);

CREATE INDEX idx_order_refund_parts_refund ON order_refund_parts (order_refund_id);

-- A card refund for an order draws on money the order refund already moved
-- into refunds_payable.
ALTER TABLE refunds DROP CONSTRAINT refunds_source_check;
ALTER TABLE refunds ADD CONSTRAINT refunds_source_check CHECK (source IN ('wallet', 'unapplied', 'order'));
