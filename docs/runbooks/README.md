# OpenPay runbooks

For whoever is on call. Each runbook starts from the signal you will actually
see (an alert, a log line, a support ticket), says how to confirm it, what to
do, and what **not** to do.

| Runbook | Typical signal |
|---|---|
| [Stuck payment](stuck-payment.md) | "I paid but my wallet didn't update" |
| [Provider outage](provider-outage.md) | success rate drops; `circuit is open until …` in logs |
| [Webhook storm](webhook-storm.md) | spike in `/openpay/v1/webhooks/*`; event backlog grows |
| [Ledger drift](ledger-drift.md) | `LEDGER DRIFT:` in logs; a failed ledger check run |
| [Recon break backlog](recon-break-backlog.md) | `RECON: … open breaks are older than …`; suspense alert |
| [Key compromise](key-compromise.md) | a secret leaked, or might have |

## Rules that apply everywhere

1. **Never edit ledger rows.** `ledger_journals` and `ledger_postings` are
   append-only (triggers refuse UPDATE/DELETE). Every correction is a new
   journal: a reversal, an adjustment (`POST /wallets/{id}/adjustments`) or a
   recon resolution. If you find yourself writing SQL against the ledger,
   stop.
2. **The provider is the source of truth for whether money moved.** Webhooks
   are hints; OpenPay always asks the provider before applying anything
   (plan.md D7). To fix a payment, *sync* it — never set its status by hand.
3. **Retrying is safe.** Every mutating API call is idempotent on its
   `Idempotency-Key`, and every provider call carries our own id. Re-running
   a sync, a recon cycle or a ledger check never double-counts.
4. **Write down what you did.** Adjustments, overrides and break resolutions
   all require a reason and are audited, but the incident channel needs the
   story too.

## Access

Operator endpoints are reached through opengate (the admin console, or the
gateway with your OpenAuth session). They need permissions such as
`openpay:payments:sync` and, for platform-level actions, `openpay:scope:all`.
Examples below write `$OPENPAY` for the base URL and leave the session out.

## Where to look

- **Logs**: structured, with `request_id` and trace id. Alerts are grepable
  by prefix: `LEDGER DRIFT:`, `RECON:`.
- **Readiness**: `GET /readyz` (database reachable); `GET /healthz` (process up).
- **Reports**: `GET $OPENPAY/openpay/v1/reports/providers?from=…&to=…` for
  success rates, `GET $OPENPAY/openpay/v1/recon/summary` for exposure.
