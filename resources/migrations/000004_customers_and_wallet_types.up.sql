-- Migration: 000004_customers_and_wallet_types
-- Customers (platform-wide people) and wallet types (what a balance is allowed
-- to do). The constraints here are the plan's D10 rules written where they
-- cannot be bypassed.

-- A customer is a person, not a per-product record: every product identifies
-- people by the same OpenAuth user id, so there is deliberately no product_id.
CREATE TABLE customers (
    id           BIGSERIAL   PRIMARY KEY,
    public_id    TEXT        NOT NULL,

    -- The OpenAuth user id. One row per person, platform-wide.
    external_ref TEXT        NOT NULL,

    -- active: normal. blocked: no new activity. merged: a duplicate that now
    -- resolves to merged_into_customer_id, kept as a tombstone rather than
    -- deleted so its external_ref keeps resolving to the survivor.
    status       TEXT        NOT NULL CHECK (status IN ('active', 'blocked', 'merged')),

    merged_into_customer_id BIGINT REFERENCES customers (id),

    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_customers_public_id    UNIQUE (public_id),
    CONSTRAINT uq_customers_external_ref UNIQUE (external_ref),

    -- A merged customer must say what it merged into, and only a merged one may.
    CONSTRAINT ck_customers_merge_target CHECK (
        (status = 'merged') = (merged_into_customer_id IS NOT NULL)
    ),
    -- Merging a customer into itself would make lookups loop forever.
    CONSTRAINT ck_customers_no_self_merge CHECK (merged_into_customer_id <> id)
);

CREATE TRIGGER trg_customers_updated_at
BEFORE UPDATE ON customers
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A wallet type is the configuration a balance behaves by. The engine has no
-- "coins" or "cashback" special cases; those names are just rows here.
CREATE TABLE wallet_types (
    id         BIGSERIAL   PRIMARY KEY,
    public_id  TEXT        NOT NULL,

    -- NULL means platform scope: one balance spendable across every product.
    product_id BIGINT      REFERENCES products (id),
    scope      TEXT        NOT NULL CHECK (scope IN ('product', 'platform')),

    code       TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    currency   CHAR(3)     NOT NULL,

    -- Capabilities. Every one of these changes what operations are legal, and
    -- together they are what distinguishes a MAIN wallet from a BONUS one.
    fundable             BOOLEAN NOT NULL,
    grantable            BOOLEAN NOT NULL,
    withdrawable         BOOLEAN NOT NULL,
    transferable         BOOLEAN NOT NULL,
    refundable_to_source BOOLEAN NOT NULL,
    allow_negative       BOOLEAN NOT NULL,

    expiry_policy TEXT    NOT NULL CHECK (expiry_policy IN ('none', 'fixed', 'rolling')),
    expiry_days   INTEGER,

    -- Limits in minor units. NULL means no limit.
    max_balance      BIGINT,
    max_txn_amount   BIGINT,
    daily_load_limit BIGINT,

    status TEXT NOT NULL CHECK (status IN ('active', 'archived')),

    -- Enabling withdrawal moves the company from closed-loop into prepaid
    -- instrument territory, so it records who signed off rather than being an
    -- unremarkable boolean someone ticked.
    withdrawable_approved_by  TEXT,
    withdrawable_approved_at  TIMESTAMPTZ,
    withdrawable_approval_ref TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_wallet_types_public_id UNIQUE (public_id),

    -- Scope and product_id are two spellings of one fact; they must agree.
    CONSTRAINT ck_wallet_types_scope CHECK (
        (scope = 'platform' AND product_id IS NULL) OR
        (scope = 'product'  AND product_id IS NOT NULL)
    ),

    -- Cashing out money nobody paid in is not a wallet, it is a leak.
    CONSTRAINT ck_wallet_types_withdrawable_requires_fundable CHECK (
        NOT withdrawable OR fundable
    ),

    -- Withdrawal without recorded approval is exactly what D10 forbids.
    CONSTRAINT ck_wallet_types_withdrawable_requires_approval CHECK (
        NOT withdrawable OR (withdrawable_approved_by IS NOT NULL AND withdrawable_approved_at IS NOT NULL)
    ),

    -- A balance must be able to hold value somehow, or it can never be non-zero.
    CONSTRAINT ck_wallet_types_fundable_or_grantable CHECK (fundable OR grantable),

    -- An expiry policy without a period is not a policy.
    CONSTRAINT ck_wallet_types_expiry_days CHECK (
        (expiry_policy = 'none' AND expiry_days IS NULL) OR
        (expiry_policy <> 'none' AND expiry_days IS NOT NULL AND expiry_days > 0)
    ),

    CONSTRAINT ck_wallet_types_limits CHECK (
        (max_balance      IS NULL OR max_balance      > 0) AND
        (max_txn_amount   IS NULL OR max_txn_amount   > 0) AND
        (daily_load_limit IS NULL OR daily_load_limit > 0)
    )
);

-- Two unique indexes rather than one constraint: PostgreSQL treats NULLs as
-- distinct, so UNIQUE (product_id, code) would happily allow two platform-wide
-- types both called MAIN.
CREATE UNIQUE INDEX uq_wallet_types_product_code
    ON wallet_types (product_id, code) WHERE product_id IS NOT NULL;
CREATE UNIQUE INDEX uq_wallet_types_platform_code
    ON wallet_types (code) WHERE product_id IS NULL;

CREATE TRIGGER trg_wallet_types_updated_at
BEFORE UPDATE ON wallet_types
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
