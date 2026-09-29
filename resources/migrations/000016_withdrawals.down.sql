ALTER TABLE provider_events DROP CONSTRAINT provider_events_object_kind_check;
ALTER TABLE provider_events ADD CONSTRAINT provider_events_object_kind_check
    CHECK (object_kind IN ('payment', 'refund', 'dispute'));
DROP TABLE IF EXISTS withdrawals;
DROP TABLE IF EXISTS beneficiaries;
