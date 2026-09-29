-- Migration: 000018_provider_controls
-- An operator's hand on routing: take a provider out of rotation, or force
-- every new attempt to one — without a deploy, and seen by every process.
CREATE TABLE provider_controls (
    provider   TEXT        PRIMARY KEY,
    -- disabled: no new attempts. forced: every new attempt, whatever its
    -- priority or health. Neither: normal routing.
    disabled   BOOLEAN     NOT NULL DEFAULT FALSE,
    forced     BOOLEAN     NOT NULL DEFAULT FALSE,
    reason     TEXT        NOT NULL,
    updated_by TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT ck_provider_controls_one CHECK (NOT (disabled AND forced))
);
-- At most one provider can be forced at a time.
CREATE UNIQUE INDEX uq_provider_controls_forced ON provider_controls (forced) WHERE forced;
