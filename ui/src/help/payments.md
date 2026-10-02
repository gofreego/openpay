# Payments

Every checkout, and where its money went.

![The payments list](payments.png)

## Filters

- **Quick views**: *All*, *Stuck (pending)*, *Authorized*, *Failed*, *Disputed*. *Stuck* is the one to watch: the poller should close pending payments within minutes, so anything old here needs a sync.
- **Status**: any single status.
- **Customer id**: one customer's payments (`cus_…`).
- The sidebar **Product** filter also applies.

**Export CSV** downloads the rows on the current page.

## Columns

*For* is a wallet top-up or an order. *Amount* is what was asked. *Provider* is who processed it. Click a row to open the payment.

## The payment page

![A payment](payment-detail.png)

- **Summary**: asked, captured and refunded amounts, and **where the money went**:
  - *Applied*: credited to the wallet or order, as normal.
  - *Unapplied*: captured, but the wallet refused it (for example a limit). The money is owed back, so refund it with reason *unapplied payment*.
  - *Suspense*: the provider's figures disagreed with ours. Reconciliation will sort it out.
- **Attempts**: each try at a provider, why that provider was chosen, and the provider's own id.
- **Timeline**: state changes, webhooks received and calls we made, in order. Click a webhook or call to see its payload. A failed one is marked *error*.
- **Refunds** made against this payment, and **History**.

### Actions

| Button | Who | What it does |
|---|---|---|
| **Sync from provider** | `openpay:payments:sync` | Asks the provider for the payment's real state and applies it. Safe to repeat. Use it on stuck payments. |
| **Refund** | `openpay:refunds:create` | Sends money back to the card or bank. Asks you to retype the amount, pick a reason and write a note. |
