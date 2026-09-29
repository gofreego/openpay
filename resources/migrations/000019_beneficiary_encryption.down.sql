DROP INDEX IF EXISTS uq_beneficiaries_destination;
CREATE UNIQUE INDEX uq_beneficiaries_destination
    ON beneficiaries (customer_id, kind, COALESCE(account_number, ''), COALESCE(ifsc, ''), COALESCE(vpa, ''));
ALTER TABLE beneficiaries DROP COLUMN IF EXISTS account_fingerprint, DROP COLUMN IF EXISTS account_last4;
