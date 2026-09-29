-- Migration: 000021_attempt_payment_index
-- Every payment read lists its attempts, but only live attempts were indexed
-- by payment (uq_payment_attempts_one_live). Finished ones were found by a
-- scan of the whole table — invisible in tests, linear in history.
CREATE INDEX idx_payment_attempts_payment ON payment_attempts (payment_id, id);
