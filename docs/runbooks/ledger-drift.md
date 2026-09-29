# Ledger drift detected

**Signal:** `LEDGER DRIFT: N invariant violations found` in the logs, or a
ledger check run with status failed. The worker checks at startup and every
`Worker.LedgerCheckInterval` (24h).

This is the most serious alert OpenPay has. The ledger is the record of what
we owe every customer; drift means some figure no longer follows from the
postings. **Page the owning engineer; do not fix it alone.**

## Confirm

```
GET  $OPENPAY/openpay/v1/ledger/checks          (openpay:ledger:read)  — recent runs and findings
POST $OPENPAY/openpay/v1/ledger/checks          (openpay:ledger:check) — run now
```

Re-run once. The checks read one consistent snapshot, so a finding that
persists is real, not a race with in-flight postings.

## What each finding means

| Finding | Meaning | Likely cause |
|---|---|---|
| `journal_unbalanced` | A journal's debits ≠ credits | A bug bypassing `PostJournal`, or manual SQL |
| `journal_too_few_postings` | A journal with under two legs | As above |
| `ledger_unbalanced` | The whole ledger does not sum to zero | Follows from one of the above |
| `balance_drift` | A cached balance ≠ the sum of its postings | A balance row written outside the posting path |
| `running_balance_drift` | A posting's `balance_after` ≠ the running sum | Postings written out of lock order |
| `posting_currency_mismatch` | A posting's currency ≠ its account's | A bug choosing the wrong account |
| `held_drift` | `held` ≠ the sum of active holds | A hold released or captured outside the engine |
| `negative_balance` | Below zero on an account that may not be | Overdraft check bypassed |
| `missing_balance_row` | An account with postings but no balance row | Interrupted migration or manual insert |

## Act

1. **Contain.** If the finding touches customer wallets and could still be
   growing (repeat runs show more), stop the source: roll back the most
   recent deploy, or freeze affected wallets. Frozen wallets refuse customer
   operations but still take adjustments.
2. **Find when.** `GET /ledger/journals/{id}` and
   `GET /ledger/accounts/{id}/statement` show the postings around the drift;
   the first bad `balance_after` dates it. Match it to deploys and to
   `audit_log`.
3. **Correct with journals, never SQL.** Once the true figure is agreed
   (engineering + finance), post the correction: a reversal of the bad
   journal, or `POST /wallets/{id}/adjustments` with reason
   `error_correction` and a memo naming the incident. Cached-balance-only
   drift (`balance_drift`, `held_drift`) is a code fix plus a recompute, not
   money movement — ask the owning engineer.
4. **Re-run the check** until it passes, and attach the passing run id to the
   incident.

## Do not

- Do not UPDATE or DELETE ledger rows; the triggers refuse it anyway, and
  disabling them to force it destroys the audit trail.
- Do not "balance it out" with a journal to a random account. Every
  correction must say whose money and why.
