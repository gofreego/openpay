-- Migration: 000009_payments
-- Collecting money through a PSP. A payment is our intent to collect; each
-- attempt is one try at one provider; provider events are what the provider
-- told us, kept verbatim.

CREATE TABLE payments (
    id           BIGSERIAL   PRIMARY KEY,
    public_id    TEXT        NOT NULL,

    -- The product whose checkout collects this money. Fees and, for a
    -- platform wallet, the top-up itself are attributed here (plan.md Part II).
    product_id   BIGINT      NOT NULL REFERENCES products (id),

    -- Nullable: a one-off guest purchase needs no customer record. A wallet
    -- top-up always has one (checked below).
    customer_id  BIGINT      REFERENCES customers (id),

    purpose      TEXT        NOT NULL CHECK (purpose IN ('wallet_topup', 'order')),
    wallet_id    BIGINT      REFERENCES wallets (id),

    -- What we asked for. What was actually captured is captured_amount, from
    -- the provider, never from the client (plan.md D7).
    amount       BIGINT      NOT NULL CHECK (amount > 0),
    currency     CHAR(3)     NOT NULL,

    -- plan.md D6. Terminal states are captured-and-beyond, failed, expired
    -- and cancelled; the state machine in code decides which moves are legal.
    status       TEXT        NOT NULL CHECK (status IN (
                     'created', 'pending', 'authorized', 'captured', 'settled',
                     'failed', 'expired', 'cancelled')),

    -- Where the captured money went in the ledger:
    --   pending   — not captured yet
    --   applied   — credited to its purpose (the wallet)
    --   unapplied — the purpose refused it (a wallet limit); owed back to the
    --               customer in refunds_payable
    --   suspense  — the provider's figures disagreed with ours; parked in the
    --               provider's suspense account for a person to resolve
    application  TEXT        NOT NULL DEFAULT 'pending' CHECK (application IN (
                     'pending', 'applied', 'unapplied', 'suspense')),

    provider        TEXT,
    captured_amount BIGINT,
    captured_at     TIMESTAMPTZ,

    failure_code    TEXT,
    failure_reason  TEXT,

    description  TEXT        NOT NULL DEFAULT '',
    return_url   TEXT        NOT NULL DEFAULT '',

    -- An unpaid payment past this point is expired by the sweeper.
    expires_at   TIMESTAMPTZ NOT NULL,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_payments_public_id UNIQUE (public_id),
    CONSTRAINT ck_payments_topup_target CHECK (
        purpose <> 'wallet_topup' OR (wallet_id IS NOT NULL AND customer_id IS NOT NULL)
    ),
    CONSTRAINT ck_payments_captured CHECK (
        (status IN ('captured', 'settled')) = (captured_amount IS NOT NULL)
    )
);

CREATE INDEX idx_payments_product  ON payments (product_id, id DESC);
CREATE INDEX idx_payments_customer ON payments (customer_id, id DESC) WHERE customer_id IS NOT NULL;
-- The poller and the expiry sweeper only ever look at unfinished payments.
CREATE INDEX idx_payments_open     ON payments (updated_at) WHERE status IN ('created', 'pending', 'authorized');

CREATE TRIGGER trg_payments_updated_at
BEFORE UPDATE ON payments
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One try at one provider. Failover creates a new attempt rather than moving
-- an in-flight one: once a customer is on a provider's checkout, that attempt
-- lives and dies there (plan.md phase 5).
CREATE TABLE payment_attempts (
    id                  BIGSERIAL   PRIMARY KEY,
    public_id           TEXT        NOT NULL,
    payment_id          BIGINT      NOT NULL REFERENCES payments (id),

    provider            TEXT        NOT NULL,
    -- Why this provider was chosen, recorded on every attempt.
    routing_reason      TEXT        NOT NULL,
    -- The provider's id for it. NULL until the provider has answered.
    provider_payment_id TEXT,

    status              TEXT        NOT NULL CHECK (status IN (
                            'created', 'pending', 'authorized', 'captured',
                            'failed', 'cancelled', 'expired')),

    checkout_url        TEXT,
    failure_code        TEXT,
    failure_reason      TEXT,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_payment_attempts_public_id UNIQUE (public_id)
);

-- A provider's id resolves to exactly one of our attempts — this is how a
-- webhook finds its payment.
CREATE UNIQUE INDEX uq_payment_attempts_provider_ref
    ON payment_attempts (provider, provider_payment_id) WHERE provider_payment_id IS NOT NULL;
-- At most one live attempt per payment, or a customer could pay twice.
CREATE UNIQUE INDEX uq_payment_attempts_one_live
    ON payment_attempts (payment_id) WHERE status IN ('created', 'pending', 'authorized');

CREATE TRIGGER trg_payment_attempts_updated_at
BEFORE UPDATE ON payment_attempts
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Every state change a payment went through, and what caused it. Append-only:
-- this is what answers "why is this payment failed?" months later.
CREATE TABLE payment_transitions (
    id          BIGSERIAL   PRIMARY KEY,
    payment_id  BIGINT      NOT NULL REFERENCES payments (id),
    from_status TEXT        NOT NULL,
    to_status   TEXT        NOT NULL,
    -- api, webhook, poller, sweeper or operator.
    source      TEXT        NOT NULL,
    -- The provider event or request behind it, when there is one.
    reference   TEXT,
    detail      TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payment_transitions_payment ON payment_transitions (payment_id, id);

CREATE TRIGGER trg_payment_transitions_immutable
BEFORE UPDATE OR DELETE ON payment_transitions
FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();

-- What a provider sent us, verbatim. Stored before anything is done with it,
-- so a webhook is never lost to a processing bug, and deduplicated on the
-- provider's event id because webhooks are at-least-once (plan.md D4).
CREATE TABLE provider_events (
    id                  BIGSERIAL   PRIMARY KEY,
    provider            TEXT        NOT NULL,
    event_id            TEXT        NOT NULL,
    event_type          TEXT        NOT NULL,
    provider_payment_id TEXT,
    payload             BYTEA       NOT NULL,
    received_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Processing state: the only columns that ever change.
    processed_at        TIMESTAMPTZ,
    attempts            INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error          TEXT,

    CONSTRAINT uq_provider_events UNIQUE (provider, event_id)
);

CREATE INDEX idx_provider_events_pending ON provider_events (next_attempt_at) WHERE processed_at IS NULL;

-- The raw event is evidence; only its processing state may change.
CREATE OR REPLACE FUNCTION reject_provider_event_rewrite()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.provider <> OLD.provider OR NEW.event_id <> OLD.event_id
       OR NEW.event_type <> OLD.event_type OR NEW.payload <> OLD.payload
       OR NEW.provider_payment_id IS DISTINCT FROM OLD.provider_payment_id
       OR NEW.received_at <> OLD.received_at THEN
        RAISE EXCEPTION 'provider event % is evidence and cannot be rewritten', OLD.id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_provider_events_immutable
BEFORE UPDATE ON provider_events
FOR EACH ROW EXECUTE FUNCTION reject_provider_event_rewrite();

CREATE TRIGGER trg_provider_events_no_delete
BEFORE DELETE ON provider_events
FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();

-- Every call we made to a provider and what came back. Written outside the
-- payment's transaction, so a call whose transaction rolled back is still on
-- record — those are exactly the calls worth investigating. Secrets never
-- reach here: providers log only the fields they choose to.
CREATE TABLE provider_request_log (
    id          BIGSERIAL   PRIMARY KEY,
    provider    TEXT        NOT NULL,
    operation   TEXT        NOT NULL,
    payment_id  TEXT,
    request     JSONB,
    response    JSONB,
    error       TEXT,
    duration_ms INTEGER     NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_provider_request_log_payment ON provider_request_log (payment_id, id) WHERE payment_id IS NOT NULL;
