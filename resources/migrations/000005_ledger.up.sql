-- Migration: 000005_ledger
-- The double-entry ledger. Every balance OpenPay reports is derived from these
-- rows; nothing else is the source of truth for money.

-- Sign convention (plan.md D2): a posting contributes +amount when it debits
-- and -amount when it credits, so raw_balance is simply their sum and the
-- posting engine never branches on account type. The human-facing figure is
-- raw_balance * normal_sign(type), where assets and expenses are +1 and
-- liabilities, income and equity are -1. A customer wallet is a LIABILITY: we
-- owe the customer, so crediting it makes what we owe go up.
CREATE TABLE ledger_accounts (
    id         BIGSERIAL   PRIMARY KEY,
    public_id  TEXT        NOT NULL,

    -- Stable, readable identity, e.g. wallet:cus_9:zshala:MAIN or
    -- psp:razorpay:receivable. Unique across the whole ledger.
    code       TEXT        NOT NULL,

    -- NULL for platform accounts: PSP receivables, bank, fee expense and tax
    -- belong to the company, not to any one product.
    product_id BIGINT      REFERENCES products (id),

    type       TEXT        NOT NULL CHECK (type IN ('asset', 'liability', 'equity', 'income', 'expense')),
    currency   CHAR(3)     NOT NULL,

    owner_kind TEXT        NOT NULL CHECK (owner_kind IN ('platform', 'customer', 'provider', 'merchant')),
    owner_id   TEXT,

    -- Off by default. Only accounts that are genuinely allowed to go negative
    -- in their natural direction may set it.
    allow_negative BOOLEAN NOT NULL DEFAULT FALSE,

    status     TEXT        NOT NULL CHECK (status IN ('active', 'closed')),

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_ledger_accounts_public_id UNIQUE (public_id),
    CONSTRAINT uq_ledger_accounts_code      UNIQUE (code)
);

CREATE INDEX idx_ledger_accounts_product ON ledger_accounts (product_id) WHERE product_id IS NOT NULL;
CREATE INDEX idx_ledger_accounts_owner   ON ledger_accounts (owner_kind, owner_id);

CREATE TRIGGER trg_ledger_accounts_updated_at
BEFORE UPDATE ON ledger_accounts
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A journal is one balanced transaction. It has no updated_at because it is
-- never updated.
CREATE TABLE ledger_journals (
    id         BIGSERIAL   PRIMARY KEY,
    public_id  TEXT        NOT NULL,

    -- The idempotency guarantee, e.g. "payment:pay_123:capture". This unique
    -- constraint is what makes double-crediting a wallet impossible; the
    -- surrounding code is bookkeeping around it.
    external_id TEXT       NOT NULL,

    kind       TEXT        NOT NULL,
    product_id BIGINT      REFERENCES products (id),

    -- What caused this journal, for tracing a balance back to its reason.
    source_kind TEXT,
    source_id   TEXT,

    -- Corrections are new journals pointing at the original, never edits.
    reverses_journal_id BIGINT REFERENCES ledger_journals (id),

    memo       TEXT,
    posted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_ledger_journals_public_id   UNIQUE (public_id),
    CONSTRAINT uq_ledger_journals_external_id UNIQUE (external_id),
    CONSTRAINT ck_ledger_journals_no_self_reversal CHECK (reverses_journal_id <> id)
);

CREATE INDEX idx_ledger_journals_source   ON ledger_journals (source_kind, source_id);
CREATE INDEX idx_ledger_journals_reverses ON ledger_journals (reverses_journal_id) WHERE reverses_journal_id IS NOT NULL;
CREATE INDEX idx_ledger_journals_product  ON ledger_journals (product_id, id DESC) WHERE product_id IS NOT NULL;

CREATE TABLE ledger_postings (
    id         BIGSERIAL   PRIMARY KEY,
    journal_id BIGINT      NOT NULL REFERENCES ledger_journals (id),
    account_id BIGINT      NOT NULL REFERENCES ledger_accounts (id),

    -- 1 debits, -1 credits. Stored as the multiplier so summing postings gives
    -- the raw balance directly.
    direction  SMALLINT    NOT NULL CHECK (direction IN (1, -1)),

    -- Always positive; direction carries the sign. A zero posting moves
    -- nothing and only obscures the journal.
    amount     BIGINT      NOT NULL CHECK (amount > 0),
    currency   CHAR(3)     NOT NULL,

    seq        INTEGER     NOT NULL,

    -- The account's raw balance immediately after this posting, so a statement
    -- shows a running balance without re-summing history.
    balance_after BIGINT   NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_ledger_postings_journal_seq UNIQUE (journal_id, seq)
);

-- Statements read one account newest-first; this is the index that serves them.
CREATE INDEX idx_ledger_postings_account ON ledger_postings (account_id, id DESC);

-- Materialised balance, updated in the same transaction as the postings.
-- Kept because summing an account's whole history on every read does not
-- survive contact with a growing ledger; the invariant checker is what keeps
-- it honest against the postings.
CREATE TABLE ledger_balances (
    account_id  BIGINT      PRIMARY KEY REFERENCES ledger_accounts (id),

    -- Sum of direction * amount over the account's postings.
    raw_balance BIGINT      NOT NULL DEFAULT 0,

    -- Reserved but not yet posted. Available balance is the natural balance
    -- minus this.
    held        BIGINT      NOT NULL DEFAULT 0 CHECK (held >= 0),

    version     BIGINT      NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The ledger is append-only (plan.md D3), enforced here rather than by
-- convention. A trigger is the difference between "we do not edit the ledger"
-- and "the ledger cannot be edited" — including by a migration, a console
-- session or a well-meant fix at 3am.
CREATE OR REPLACE FUNCTION reject_ledger_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION
        'ledger rows are append-only: % on % is not permitted. Correct with a reversing journal instead.',
        TG_OP, TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ledger_journals_immutable
BEFORE UPDATE OR DELETE ON ledger_journals
FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();

CREATE TRIGGER trg_ledger_postings_immutable
BEFORE UPDATE OR DELETE ON ledger_postings
FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();
