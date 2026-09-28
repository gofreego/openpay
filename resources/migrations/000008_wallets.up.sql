-- Migration: 000008_wallets
-- A wallet is a customer's balance of one wallet type. It holds no balance of
-- its own: it points at exactly one ledger account (a LIABILITY — we owe the
-- customer), and the balance is whatever the ledger says (plan.md D2).

CREATE TABLE wallets (
    id             BIGSERIAL   PRIMARY KEY,
    public_id      TEXT        NOT NULL,

    customer_id    BIGINT      NOT NULL REFERENCES customers (id),
    wallet_type_id BIGINT      NOT NULL REFERENCES wallet_types (id),

    -- Copied from the wallet type, which never changes product, so product
    -- scoping (D8) is one indexed column rather than a join on every read.
    -- NULL for a platform-scoped type, spendable in every product.
    product_id     BIGINT      REFERENCES products (id),

    ledger_account_id BIGINT   NOT NULL REFERENCES ledger_accounts (id),

    -- frozen stops customer-initiated movement while support investigates;
    -- operator adjustments still work, since they are how a frozen wallet is
    -- put right.
    status         TEXT        NOT NULL CHECK (status IN ('active', 'frozen')),

    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_wallets_public_id      UNIQUE (public_id),
    -- One wallet per customer per type. This is what makes lazy creation on
    -- first reference safe to race.
    CONSTRAINT uq_wallets_customer_type  UNIQUE (customer_id, wallet_type_id),
    -- A ledger account backs one wallet at most, or two wallets would share a
    -- balance.
    CONSTRAINT uq_wallets_ledger_account UNIQUE (ledger_account_id)
);

CREATE INDEX idx_wallets_customer ON wallets (customer_id);
CREATE INDEX idx_wallets_product  ON wallets (product_id) WHERE product_id IS NOT NULL;

CREATE TRIGGER trg_wallets_updated_at
BEFORE UPDATE ON wallets
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Why an operator or product moved money that no payment explains: grants and
-- adjustments carry a code from a fixed list (plan.md U-D7), so "how much did
-- we give away as goodwill last month?" is a query, not a text search through
-- memos. Adding a nullable column rewrites no ledger rows.
ALTER TABLE ledger_journals ADD COLUMN reason_code TEXT;

CREATE INDEX idx_ledger_journals_reason ON ledger_journals (reason_code) WHERE reason_code IS NOT NULL;
