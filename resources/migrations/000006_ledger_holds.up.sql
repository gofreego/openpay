-- Migration: 000006_ledger_holds
-- Holds reserve part of an account's balance without moving it. A split-tender
-- checkout holds the wallet share while the card payment is in flight, then
-- either captures it (posts a journal) or releases it (posts nothing).

-- Unlike journals and postings, a hold is mutable: its status moves once, from
-- active to a terminal state. What it never does is move money by itself —
-- only the capture journal does that — so the ledger stays append-only.
CREATE TABLE ledger_holds (
    id         BIGSERIAL   PRIMARY KEY,
    public_id  TEXT        NOT NULL,

    -- Idempotency, as with journals: e.g. "order:ord_1:hold:wlt_2". Placing
    -- the same hold twice must reserve the money once.
    external_id TEXT       NOT NULL,

    account_id BIGINT      NOT NULL REFERENCES ledger_accounts (id),

    amount     BIGINT      NOT NULL CHECK (amount > 0),
    currency   CHAR(3)     NOT NULL,

    status     TEXT        NOT NULL CHECK (status IN ('active', 'captured', 'released', 'expired')),

    -- An abandoned checkout must not strand customer funds forever. The
    -- sweeper releases active holds past this point.
    expires_at TIMESTAMPTZ NOT NULL,

    -- Set on capture: the journal that actually moved the money, and how much
    -- of the hold it took. The remainder simply stops being reserved.
    capture_journal_id BIGINT REFERENCES ledger_journals (id),
    captured_amount    BIGINT,

    resolved_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_ledger_holds_public_id   UNIQUE (public_id),
    CONSTRAINT uq_ledger_holds_external_id UNIQUE (external_id),

    -- A captured hold says which journal captured it and how much, and only a
    -- captured one does.
    CONSTRAINT ck_ledger_holds_capture CHECK (
        (status = 'captured') = (capture_journal_id IS NOT NULL AND captured_amount IS NOT NULL)
    ),
    CONSTRAINT ck_ledger_holds_captured_amount CHECK (
        captured_amount IS NULL OR (captured_amount > 0 AND captured_amount <= amount)
    ),
    CONSTRAINT ck_ledger_holds_resolved CHECK (
        (status = 'active') = (resolved_at IS NULL)
    )
);

CREATE INDEX idx_ledger_holds_account ON ledger_holds (account_id) WHERE status = 'active';
-- Serves the expiry sweeper, which only ever looks at active holds.
CREATE INDEX idx_ledger_holds_expiry  ON ledger_holds (expires_at) WHERE status = 'active';

CREATE TRIGGER trg_ledger_holds_updated_at
BEFORE UPDATE ON ledger_holds
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A resolved hold is history. Reopening one would reserve money a second time
-- against a decision already made.
CREATE OR REPLACE FUNCTION reject_resolved_hold_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status <> 'active' THEN
        RAISE EXCEPTION 'ledger hold % is already %; a resolved hold cannot change', OLD.public_id, OLD.status;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ledger_holds_resolved_immutable
BEFORE UPDATE ON ledger_holds
FOR EACH ROW EXECUTE FUNCTION reject_resolved_hold_mutation();

CREATE TRIGGER trg_ledger_holds_no_delete
BEFORE DELETE ON ledger_holds
FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();
