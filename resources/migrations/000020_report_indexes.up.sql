-- Migration: 000020_report_indexes
-- Period reads on one account — a P&L line, a statement export — filter
-- postings by time. The only postings index was (account_id, id DESC), so
-- each of those read the account's whole history to find a month of it.
-- With this they read just the period, and the id keeps rows in posting
-- order within a timestamp (one transaction's postings share NOW()).
CREATE INDEX idx_ledger_postings_account_time ON ledger_postings (account_id, created_at, id);

-- Provider success rates count attempts started in a period.
CREATE INDEX idx_payment_attempts_created ON payment_attempts (created_at);
