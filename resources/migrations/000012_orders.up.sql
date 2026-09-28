-- Migration: 000012_orders
-- What was bought and how it was paid for. OpenPay records the amounts the
-- product sends; it prices nothing and computes no tax (plan.md D13).

-- The item catalogue is for description and reporting. An order carries the
-- amounts actually charged, so a price change never rewrites what a customer
-- was billed.
CREATE TABLE items (
    id              BIGSERIAL   PRIMARY KEY,
    public_id       TEXT        NOT NULL,
    product_id      BIGINT      NOT NULL REFERENCES products (id),
    code            TEXT        NOT NULL,
    name            TEXT        NOT NULL,
    reference_price BIGINT      NOT NULL CHECK (reference_price >= 0),
    currency        CHAR(3)     NOT NULL,
    -- A label for reporting, e.g. "gst_18". Never used to compute anything.
    tax_class       TEXT        NOT NULL DEFAULT '',
    status          TEXT        NOT NULL CHECK (status IN ('active', 'archived')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_items_public_id    UNIQUE (public_id),
    CONSTRAINT uq_items_product_code UNIQUE (product_id, code)
);

CREATE TRIGGER trg_items_updated_at
BEFORE UPDATE ON items
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE orders (
    id          BIGSERIAL   PRIMARY KEY,
    public_id   TEXT        NOT NULL,

    -- One order, one product, always: a mixed-product order is exactly how
    -- revenue lands against the wrong product (plan.md D8).
    product_id  BIGINT      NOT NULL REFERENCES products (id),
    -- Nullable: a guest purchase needs no customer record.
    customer_id BIGINT      REFERENCES customers (id),

    -- The product's own id for this order: idempotency across retries, and
    -- how a support case finds it.
    external_ref TEXT       NOT NULL,
    -- The product's invoice number, for cross-reference. OpenPay is not the
    -- invoice system of record (plan.md Q5).
    invoice_ref  TEXT       NOT NULL DEFAULT '',

    currency    CHAR(3)     NOT NULL,
    subtotal    BIGINT      NOT NULL CHECK (subtotal >= 0),
    discount    BIGINT      NOT NULL DEFAULT 0 CHECK (discount >= 0),
    tax         BIGINT      NOT NULL DEFAULT 0 CHECK (tax >= 0),
    total       BIGINT      NOT NULL CHECK (total > 0),
    -- As the product sent it, e.g. "18%". Recorded, never used.
    tax_rate    TEXT        NOT NULL DEFAULT '',
    -- False when the product sent a bare total: the revenue is booked gross,
    -- and this says so rather than letting it pass as tax-free (D13).
    tax_breakdown_provided BOOLEAN NOT NULL,

    status      TEXT        NOT NULL CHECK (status IN ('pending_payment', 'paid', 'failed', 'cancelled')),
    failure_reason TEXT,

    -- The card (gateway) share, if any, and the payment collecting it.
    gateway_amount BIGINT   NOT NULL DEFAULT 0 CHECK (gateway_amount >= 0),

    expires_at  TIMESTAMPTZ NOT NULL,
    paid_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_orders_public_id    UNIQUE (public_id),
    CONSTRAINT uq_orders_external_ref UNIQUE (product_id, external_ref),
    -- Integrity, not tax logic: the parts must add up, and the order is
    -- refused before it reaches an immutable ledger if they do not (D13).
    CONSTRAINT ck_orders_arithmetic CHECK (subtotal - discount + tax = total),
    CONSTRAINT ck_orders_discount   CHECK (discount <= subtotal),
    CONSTRAINT ck_orders_gateway    CHECK (gateway_amount <= total),
    CONSTRAINT ck_orders_paid       CHECK ((status = 'paid') = (paid_at IS NOT NULL))
);

CREATE INDEX idx_orders_customer ON orders (customer_id, id DESC) WHERE customer_id IS NOT NULL;
CREATE INDEX idx_orders_pending  ON orders (expires_at) WHERE status = 'pending_payment';

CREATE TRIGGER trg_orders_updated_at
BEFORE UPDATE ON orders
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE order_line_items (
    id          BIGSERIAL   PRIMARY KEY,
    order_id    BIGINT      NOT NULL REFERENCES orders (id),
    -- Optional: a line may describe something with no catalogue entry.
    item_id     BIGINT      REFERENCES items (id),
    description TEXT        NOT NULL,
    quantity    INTEGER     NOT NULL CHECK (quantity > 0),
    unit_amount BIGINT      NOT NULL CHECK (unit_amount >= 0),
    amount      BIGINT      NOT NULL,
    CONSTRAINT ck_order_line_items_amount CHECK (amount = quantity * unit_amount)
);

CREATE INDEX idx_order_line_items_order ON order_line_items (order_id);

-- How the order is paid: wallet shares, and at most one gateway share.
CREATE TABLE order_tenders (
    id          BIGSERIAL   PRIMARY KEY,
    order_id    BIGINT      NOT NULL REFERENCES orders (id),
    kind        TEXT        NOT NULL CHECK (kind IN ('wallet', 'gateway')),
    wallet_id   BIGINT      REFERENCES wallets (id),
    amount      BIGINT      NOT NULL CHECK (amount > 0),
    -- held: reserved while the gateway share is paid. captured: spent.
    -- released: the order failed and the reservation was let go.
    status      TEXT        NOT NULL CHECK (status IN ('held', 'captured', 'released')),
    CONSTRAINT ck_order_tenders_wallet CHECK ((kind = 'wallet') = (wallet_id IS NOT NULL))
);

CREATE INDEX idx_order_tenders_order ON order_tenders (order_id);
CREATE UNIQUE INDEX uq_order_tenders_one_gateway ON order_tenders (order_id) WHERE kind = 'gateway';

-- A payment for an order says which.
ALTER TABLE payments ADD COLUMN order_id BIGINT REFERENCES orders (id);
ALTER TABLE payments DROP CONSTRAINT ck_payments_topup_target;
ALTER TABLE payments ADD CONSTRAINT ck_payments_target CHECK (
    (purpose <> 'wallet_topup' OR (wallet_id IS NOT NULL AND customer_id IS NOT NULL)) AND
    (purpose <> 'order' OR order_id IS NOT NULL)
);
CREATE INDEX idx_payments_order ON payments (order_id) WHERE order_id IS NOT NULL;
