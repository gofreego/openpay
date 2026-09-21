-- Migration: 000003_products_and_audit
-- Products, the credentials their backends authenticate with, and the audit
-- trail covering every change an operator makes.

-- A product is one of our own apps (Zshala, BappaApp). It is the scoping
-- dimension for wallets, orders and revenue (plan.md D8).
CREATE TABLE products (
    id               BIGSERIAL   PRIMARY KEY,
    public_id        TEXT        NOT NULL,

    -- Stable, human-readable handle used in ledger account codes such as
    -- income:zshala:product_sales. Immutable once set: changing it would
    -- orphan every account code already referring to it.
    code             TEXT        NOT NULL,

    name             TEXT        NOT NULL,

    -- suspended stops new activity. Products are never deleted — money has
    -- already moved against them and the ledger must stay interpretable.
    status           TEXT        NOT NULL CHECK (status IN ('active', 'suspended')),

    default_currency CHAR(3)     NOT NULL,

    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_products_public_id UNIQUE (public_id),
    CONSTRAINT uq_products_code      UNIQUE (code)
);

CREATE TRIGGER trg_products_updated_at
BEFORE UPDATE ON products
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- How a product's backend authenticates to OpenPay. Operators authenticate
-- separately, through OpenAuth and opengate (plan.md U-D2).
CREATE TABLE service_credentials (
    id          BIGSERIAL   PRIMARY KEY,
    public_id   TEXT        NOT NULL,
    product_id  BIGINT      NOT NULL REFERENCES products (id),

    name        TEXT        NOT NULL,

    -- key_id is the public half, sent in the clear and used to find the row.
    key_id      TEXT        NOT NULL,
    -- secret_hash is argon2id. The secret itself is shown once at creation
    -- and never stored, so a database leak does not hand over the keys.
    secret_hash TEXT        NOT NULL,

    status      TEXT        NOT NULL CHECK (status IN ('active', 'revoked')),

    -- Answers "is this credential still in use?" before revoking it.
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_service_credentials_public_id UNIQUE (public_id),
    CONSTRAINT uq_service_credentials_key_id    UNIQUE (key_id)
);

CREATE INDEX idx_service_credentials_product ON service_credentials (product_id);

CREATE TRIGGER trg_service_credentials_updated_at
BEFORE UPDATE ON service_credentials
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Append-only record of who changed what. Like the ledger (plan.md D3) there
-- is no UPDATE and no DELETE: an audit trail that can be edited is not one.
CREATE TABLE audit_log (
    id            BIGSERIAL   PRIMARY KEY,

    -- operator: a person via the console. service: a product backend.
    -- system: a background worker with no human behind it.
    actor_type    TEXT        NOT NULL CHECK (actor_type IN ('operator', 'service', 'system')),
    actor_id      TEXT        NOT NULL,

    -- Dotted past tense, e.g. product.created, credential.revoked.
    action        TEXT        NOT NULL,
    resource_type TEXT        NOT NULL,
    resource_id   TEXT        NOT NULL,

    product_id    BIGINT      REFERENCES products (id),

    -- Ties the change back to its request, its logs and its trace.
    request_id    TEXT,

    -- Before and after states. Keeping both means an audit entry answers what
    -- changed, not merely that something did.
    before        JSONB,
    after         JSONB,

    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_log_resource ON audit_log (resource_type, resource_id, id DESC);
CREATE INDEX idx_audit_log_actor    ON audit_log (actor_id, id DESC);
CREATE INDEX idx_audit_log_product  ON audit_log (product_id, id DESC) WHERE product_id IS NOT NULL;
