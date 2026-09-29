# Stuck payment

**Signal:** a customer says they paid but their wallet or order did not
update; or payments sit in `pending` / `authorized` longer than usual.

## How OpenPay normally unsticks itself

- The provider's webhook is stored in `provider_events` and processed within
  a second (`Worker.PaymentEventInterval`).
- If no webhook arrives, the **poller** checks every open payment untouched
  for `Payments.PollAfter` (2 min) with the provider, every
  `Worker.PaymentPollInterval` (1 min).
- A payment still open after `Payments.TTL` (30 min) is **expired** — after
  one last check with the provider, so a late capture is never thrown away.

So a payment is only really stuck if it is older than a few minutes *and*
the poller cannot reach a verdict.

## Confirm

1. Find the payment: `GET $OPENPAY/openpay/v1/payments/{id}` (or list by
   customer). Note `status`, `application`, the provider and its attempts.
2. Check the provider's own dashboard for that payment id (the attempt's
   `provider_payment_id`).
3. Check the logs for the payment id — `failed to poll payment` means the
   provider could not be asked; see [provider outage](provider-outage.md).

## Fix

**Sync it.** This asks the provider for the truth and applies it — exactly
as the poller would, but now:

```
POST $OPENPAY/openpay/v1/payments/{id}/sync        (openpay:payments:sync)
```

Then read the result:

| After sync | Meaning | Action |
|---|---|---|
| `captured`, `application = applied` | Money is in the wallet / order paid | Tell the customer; done |
| `captured`, `application = unapplied` | Captured, but the wallet refused it (a limit) | Owed back in `refunds_payable`; refund it (`POST /payments/{id}/refunds`, platform ops) |
| `captured`, `application = suspense` | Provider's figures disagree with ours (amount/currency) | Parked in `psp:<provider>:suspense`; handled by recon — see [recon break backlog](recon-break-backlog.md) |
| still `pending` | Provider has not decided (customer mid-3DS, UPI pending) | Wait; the TTL will expire it cleanly |
| `failed` / `expired` | No money moved | Customer retries; if they insist they were charged, check the provider — a capture there will be picked up by the next sync or by settlement recon |

## Do not

- Do not credit the wallet by adjustment "because the customer showed a
  screenshot". If the provider captured, a sync applies it; if you adjust
  *and* it later syncs, the customer is paid twice.
- Do not change `payments.status` in SQL. The state machine and the ledger
  would disagree, and the invariant check will not catch a status column.
