-- Migration: 000007_ledger_check_runs
-- A record of every ledger invariant check, scheduled or manual. The latest
-- run is what the dashboard shows as "ledger drift status", and the history is
-- what answers "since when has this been wrong?".
CREATE TABLE ledger_check_runs (
    id           BIGSERIAL   PRIMARY KEY,
    public_id    TEXT        NOT NULL,

    -- ok: every invariant held. drift: at least one did not. error: the check
    -- could not complete, which says nothing either way.
    status       TEXT        NOT NULL CHECK (status IN ('ok', 'drift', 'error')),

    trigger      TEXT        NOT NULL CHECK (trigger IN ('scheduled', 'manual')),
    triggered_by TEXT,

    -- Bounded per invariant, so a badly broken ledger cannot produce an
    -- unbounded row; truncated says the list was cut short.
    findings     JSONB       NOT NULL DEFAULT '[]',
    truncated    BOOLEAN     NOT NULL DEFAULT FALSE,
    error        TEXT,

    started_at   TIMESTAMPTZ NOT NULL,
    finished_at  TIMESTAMPTZ NOT NULL,

    CONSTRAINT uq_ledger_check_runs_public_id UNIQUE (public_id),
    CONSTRAINT ck_ledger_check_runs_error CHECK ((status = 'error') = (error IS NOT NULL))
);
