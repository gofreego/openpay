-- Migration: 000019_beneficiary_encryption
-- Bank account numbers are stored encrypted (pkg/fieldcrypt). The last four
-- digits are kept apart for display, and a keyed fingerprint replaces the
-- plaintext in the uniqueness rule — encrypting the same number twice gives
-- different ciphertext, so the column itself can no longer be compared.
ALTER TABLE beneficiaries
    ADD COLUMN account_last4       TEXT,
    ADD COLUMN account_fingerprint TEXT;

DROP INDEX uq_beneficiaries_destination;
CREATE UNIQUE INDEX uq_beneficiaries_destination
    ON beneficiaries (customer_id, kind, COALESCE(account_fingerprint, ''), COALESCE(vpa, ''));
