# Dashboard

The dashboard answers one question: **is payments healthy right now?** Each card is green (fine), amber (look at it) or red (act now). Click a card to open the page where you can act on it.

![The dashboard](dashboard.png)

## The cards

| Card | What it tells you | Opens |
|---|---|---|
| **&lt;provider&gt; success, last hour** | Share of finished payments that a provider captured in the last hour. Green at 90% or more, amber from 70%, red below. Shows the top failure code. Only appears when the provider saw traffic. | Providers |
| **Payments pending** | Payments still waiting on the customer or the provider. The poller settles these within minutes; old ones need a manual sync. | Payments, filtered to pending |
| **Ledger** | Result of the last ledger check: *Balanced*, or *Drift* with the number of findings. Red means the books disagree with themselves. | Ledger checks |
| **Customer money held** | Total balance of fundable wallets: money we owe customers. | — |
| **Suspense** | Provider money not yet matched to a payment. Should trend to zero. | Reconciliation |
| **Recon breaks** | Open reconciliation breaks; red once any is older than 48 hours. | Reconciliation |
| **Withdrawals awaiting approval** | Cash-outs above a wallet type's threshold, waiting for a second person. | Withdrawals |
| **Open disputes** | Chargebacks waiting for our evidence. | Disputes |

You only see the cards your permissions allow. For example, the recon cards need `openpay:recon:read` with the all-products scope.

## The product filter

The **Product** selector at the bottom of the sidebar applies to every page. Central ops can choose **All products**. An operator scoped to one product sees that product, locked.

The chip under it shows who you are signed in as and whether you are *Central ops* (all products) or *Product ops*. Hover over it to see your permissions.

## Tips

- Figures load live each time you open the page; there is no auto-refresh.
- Every page has an ⓘ button next to its title that opens its guide. **All guides** lists them all.
