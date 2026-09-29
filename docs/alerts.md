# Alerts

The metrics OpenPay exports over OTLP (`Telemetry` in config) and the alerts
to build on them. Thresholds are starting points for low volume; tune them
once there is a month of real traffic. Each alert links the runbook to open
when it fires.

Operational gauges are read from the database every
`Worker.OpsMetricsInterval` (1m) rather than counted in-process, so they
report only what committed.

## Page (money is at risk or customers are affected now)

| Alert | Condition | Runbook |
|---|---|---|
| Ledger drift | `openpay.ledger.check.findings > 0` | [ledger drift](runbooks/ledger-drift.md) |
| Ledger check not running | no increase in `openpay.ledger.check.runs` for 26h | [ledger drift](runbooks/ledger-drift.md) — a check that does not run cannot find drift |
| Provider failing | `openpay.payments.success_rate_bps{provider} < 7000` for 10m **and** `openpay.payments.finished_attempts{provider} >= 20` | [provider outage](runbooks/provider-outage.md) |
| Payments stuck | `openpay.payments.stuck > 0` for 15m | [stuck payment](runbooks/stuck-payment.md) — the expiry job or poller is not keeping up |
| Webhooks not processed | `openpay.webhooks.oldest_age > 300` (s) | [webhook storm](runbooks/webhook-storm.md) |

## Ticket (needs a person today, not at 3am)

| Alert | Condition | Runbook |
|---|---|---|
| Hold leakage | `openpay.holds.overdue > 0` for 15m | Customer money reserved for nothing: the hold sweeper is failing. Check worker logs for `failed to expire hold` |
| Aged recon breaks | `openpay.recon.aged_breaks > 0` | [recon break backlog](runbooks/recon-break-backlog.md) |
| Suspense growing | `abs(openpay.recon.suspense{provider}) > ReconAlerts.SuspenseThreshold` | [recon break backlog](runbooks/recon-break-backlog.md) |
| Webhook backlog | `openpay.webhooks.backlog > 1000` | [webhook storm](runbooks/webhook-storm.md) |
| Events not publishing | `rate(openpay.outbox.publish_failures) > 0` for 15m, or `openpay.outbox.lag` p95 > 60s | Downstream consumers are behind; check the outbox publisher's target |

## Why these thresholds

- **Success rate needs a sample size.** At low volume three declined cards
  in a row read as 0%. The `finished_attempts` floor keeps a quiet night
  from paging anyone; without it, this alert gets muted and then misses the
  real outage.
- **Stuck means past expiry**, not merely open: a customer mid-UPI-approval
  is open and fine. Only payments older than `Payments.TTL` + 10 minutes
  count, which the expiry job should always have closed.
- **Drift pages on the first finding.** There is no acceptable amount of
  it.

## Dashboard

One board, top to bottom in the order someone on call reads it:

1. `openpay.payments.success_rate_bps` by provider, with
   `finished_attempts` beneath it for scale.
2. `openpay.payments.stuck`, `openpay.webhooks.backlog`,
   `openpay.webhooks.oldest_age`.
3. `openpay.recon.suspense` by provider, `openpay.recon.open_breaks`,
   `openpay.recon.aged_breaks`.
4. `openpay.ledger.check.findings`, `openpay.holds.overdue`.
5. `openpay.outbox.lag`, request latency and error rate from the HTTP and
   gRPC spans.

For trends beyond the metrics' retention, the reports API has the same
figures by period: `GET /openpay/v1/reports/providers`, `/reports/pnl`,
`/recon/summary`.
