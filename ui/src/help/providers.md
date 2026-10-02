# Providers

The payment providers (Razorpay, Cashfree…) that take card and UPI payments: their health, how new payments are routed, and the kill-switch. Provider credentials live in the secret store and are never shown here.

![Providers](providers.png)

- The banner says **where new payments go right now**, and why. A red banner means no provider can take payments: checkouts are failing.
- Each card shows the provider's **priority** and whether it is **healthy**. *Circuit open* means repeated failures paused it until the time shown; it recovers by itself.
- *Last error* and any *Override* reason are shown on the card.

## Overrides

With `openpay:providers:manage`:

| Button | Effect |
|---|---|
| **Disable** | Stop sending new payments to this provider (the kill-switch). |
| **Force** | Send every new payment to this provider, whatever its priority. |
| **Restore** | Remove the override and go back to normal priority routing. |

Each asks for a reason, which is shown on the card and recorded in the audit log.
