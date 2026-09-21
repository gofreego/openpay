-- Rollback: 000005_ledger
--
-- The immutability triggers must go first: they would otherwise refuse the
-- deletes that dropping the tables implies.

DROP TRIGGER IF EXISTS trg_ledger_postings_immutable ON ledger_postings;
DROP TRIGGER IF EXISTS trg_ledger_journals_immutable ON ledger_journals;
DROP FUNCTION IF EXISTS reject_ledger_mutation();

DROP TABLE IF EXISTS ledger_balances;
DROP TABLE IF EXISTS ledger_postings;
DROP TABLE IF EXISTS ledger_journals;

DROP TRIGGER IF EXISTS trg_ledger_accounts_updated_at ON ledger_accounts;
DROP TABLE IF EXISTS ledger_accounts;
