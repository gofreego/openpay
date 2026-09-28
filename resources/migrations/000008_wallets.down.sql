DROP INDEX IF EXISTS idx_ledger_journals_reason;
ALTER TABLE ledger_journals DROP COLUMN IF EXISTS reason_code;
DROP TABLE IF EXISTS wallets;
