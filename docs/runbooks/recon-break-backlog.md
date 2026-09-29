# Reconciliation break backlog

**Signal:** `RECON: N of M open breaks are older than 48h`, or
`RECON: <provider> suspense holds X, above the Y threshold`
(`ReconAlerts.AgedAfter`, `ReconAlerts.SuspenseThreshold`).

Reconciliation runs every `Worker.ReconInterval` (1h): it ingests each
provider's settlements, matches every line to our payments, refunds and
chargebacks, and opens a **break** for anything it cannot explain. Money it
cannot place sits in `psp:<provider>:suspense` until a person decides.

## Look

```
GET  $OPENPAY/openpay/v1/recon/summary                     (openpay:recon:read, scope:all)
GET  $OPENPAY/openpay/v1/recon/breaks?status=open          (openpay:recon:read)
GET  $OPENPAY/openpay/v1/settlements/{id}                  the settlement a break came from
POST $OPENPAY/openpay/v1/recon/cycles                      run a cycle now (openpay:recon:manage)
```

## Kinds of break, and what usually settles them

| Classification | Meaning | Usually |
|---|---|---|
| `missing_in_ledger` | Provider settled money we have no record of | A payment we never saw captured: **sync it** ([stuck payment](stuck-payment.md)), then force-match |
| `missing_at_provider` | We captured, the provider has not settled within `Recon.SettleWithin` (72h) | Timing — settlement is late; wait a cycle. Past a week, raise with the provider |
| `amount_mismatch` | Settled amount ≠ captured amount | Partial capture or provider error; confirm with the provider |
| `fee_mismatch` | Fee charged ≠ fee expected | Rate card changed; tell finance (`GET /recon/fee-variance`) |
| `duplicate` | The same payment settled twice | Provider error; claim it back from them |

## Resolve

```
POST $OPENPAY/openpay/v1/recon/breaks/{id}/resolve         (openpay:recon:manage)
```

| Action | Use when | Ledger effect |
|---|---|---|
| `BREAK_ACTION_RESOLVE` | Explained, no money parked (e.g. late settlement arrived) | None |
| `BREAK_ACTION_FORCE_MATCH` + `payment_id` | The money belongs to a payment we can name | suspense → receivable |
| `BREAK_ACTION_WRITE_OFF` | The money will never be matched or recovered | suspense (or receivable) → `expense:reconciliation_writeoffs` |

Each needs a `reason_code` (`provider_error`, `reference_mismatch`,
`timing`, `bank_adjustment`, `unrecoverable`, `other`) and a note saying
what you found. They are audited.

## Working a backlog

1. Run a cycle first; late settlements often clear `missing_at_provider`
   breaks by themselves.
2. Sort the rest by amount. Big ones first — they are what finance will ask
   about.
3. Group by cause. Twenty breaks from one provider on one day are one
   problem (a report format change, an outage), not twenty.
4. Write off only with finance's agreement, and only once the provider has
   confirmed there is nothing to recover.

## Do not

- Do not write off to make the alert go away. Suspense trending to zero by
  write-off is money lost, and the P&L will show it.
- Do not force-match to a payment you have not verified with the provider;
  a wrong match hides two errors behind one clean-looking line.
