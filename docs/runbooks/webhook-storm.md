# Webhook storm

**Signal:** a spike of requests to `/openpay/v1/webhooks/{provider}`; the
unprocessed event count climbs; CPU or DB connections rise.

## How webhooks flow

1. The handler verifies the signature, stores the event in
   `provider_events` — **unique on `(provider, event_id)`**, so a replay is a
   no-op — and answers `200`. It does no payment work in the request.
2. The worker claims events (`FOR UPDATE SKIP LOCKED`) and, treating each as
   a hint, asks the provider for the payment's real state and syncs it.
3. A failing event is retried with backoff (5s, 10s, 20s … capped at 10 min),
   with `attempts` and `last_error` recorded on the row.

Answers: `401` bad signature, `404` unknown provider, `503` could not store
(provider will redeliver), `200` stored or already had it.

## Tell which storm it is

```sql
-- backlog, and how stuck it is
SELECT COUNT(*), MIN(received_at), MAX(attempts)
FROM provider_events WHERE processed_at IS NULL;

-- what is failing, if anything
SELECT provider, event_type, last_error, COUNT(*)
FROM provider_events WHERE processed_at IS NULL AND attempts > 0
GROUP BY 1, 2, 3 ORDER BY 4 DESC LIMIT 20;
```

**A. The provider is replaying (after its own outage).** Mostly duplicates,
answered `200` without new rows. Harmless; let it drain.

**B. Real volume (a sale, a batch settlement).** New rows, processing keeps
up with a small lag. Nothing to do; watch the backlog fall.

**C. Events keep failing.** `last_error` repeats. Usually the provider API
is down (each event's sync needs it) — see
[provider outage](provider-outage.md). The backoff stops this becoming a hot
loop; once the provider recovers the backlog drains by itself.

**D. Junk traffic.** A flood of `401`s (`rejected <provider> webhook` in the
logs): someone is
posting to the webhook URL without the secret. OpenPay rejects it before
touching the database, but it still costs a TLS handshake and a signature
check each. Block the source at the load balancer / WAF. If the requests
carry **valid** signatures, the webhook secret is compromised — see
[key compromise](key-compromise.md).

## Do not

- Do not delete rows from `provider_events` to "clear the queue". They are
  the evidence of what the provider told us, and deleting an unprocessed one
  loses its hint (the poller would still catch the payment, minutes later).
- Do not mark events processed by hand. If one is permanently unprocessable
  (an object we never created), say so in the incident and leave it; its
  retry interval is already capped.
