# Provider outage

**Signal:** payment success rate for one provider drops; checkout errors;
logs show provider calls timing out or
`<provider> circuit is open until … after N failures`.

## What OpenPay already does

- Every provider call has a timeout (`Payments.Resilience.Timeout`, 10s) and
  retries outages (`Retries`, with our idempotency id, so a retry never
  double-charges).
- After `FailureThreshold` (5) consecutive outage failures the **circuit
  opens** for `Cooldown` (30s): new payments route to the next healthy
  provider by priority, then one probe call tests recovery.
- Payments already started stay with their provider — **failover never
  happens mid-payment**. The poller keeps checking them and catches up once
  the provider recovers.

With one provider configured there is nowhere to fail over to: new checkouts
fail fast with `unavailable` instead of hanging.

## Confirm

```
GET $OPENPAY/openpay/v1/providers                          (openpay:providers:read, scope:all)
GET $OPENPAY/openpay/v1/reports/providers?from=…&to=…      (openpay:payments:read)
```

`healthy: false`, a rising `consecutive_failures` and `last_error` confirm
it. Check the provider's status page.

## Act

**Partial outage the breaker keeps flapping on** (some calls succeed, so the
circuit keeps closing): take the provider out of rotation by hand.

```
POST $OPENPAY/openpay/v1/providers/{name}/override         (openpay:providers:manage, scope:all)
{"override": "PROVIDER_OVERRIDE_DISABLED", "reason": "INC-123: razorpay 5xx on card auth"}
```

This takes effect on the next routing decision in every process, with no
deploy. `PROVIDER_OVERRIDE_FORCED` sends every new attempt to one provider
(e.g. the other is degraded in a way health cannot see).

**Recovery:** confirm on the provider's status page, then

```
{"override": "PROVIDER_OVERRIDE_NONE", "reason": "INC-123: resolved, restoring normal routing"}
```

**Afterwards:** payments that were open during the outage are resolved by
the poller; anything a customer asks about, sync
([stuck payment](stuck-payment.md)). Webhooks the provider replays after
recovery are de-duplicated by `(provider, event_id)`.

## Do not

- Do not disable the only provider unless you mean to stop taking payments.
- Do not restart OpenPay to "clear" the breaker; it recovers on its own
  probe, and a restart forgets health state and sends traffic straight back.
