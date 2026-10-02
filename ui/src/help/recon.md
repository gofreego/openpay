# Reconciliation

Providers pay us in **settlements**: one bank transfer covering many payments, minus fees. Reconciliation matches every settlement line to a payment in our ledger. **Suspense** (provider money not yet matched) should trend to zero, and **every break needs a decision**.

![Reconciliation](recon.png)

## At the top

- **Open breaks**: lines that did not match.
- **Aged breaks**: open for more than 48 hours, so they need a person.
- **Per provider**: money in suspense and money captured but awaiting settlement.
- **Run cycle now** (`openpay:recon:manage`) fetches and matches settlements immediately; it otherwise runs hourly.

## Break queue

Filter by kind with the chips. Each kind shows what to do:

| Kind | Meaning | Usual action |
|---|---|---|
| **missing in ledger** | The provider settled money we have no record of. | Sync the payment, then **Match** it. |
| **missing at provider** | We captured it; the provider has not settled it yet. | Usually timing; wait. |
| **amount mismatch** | Settled amount differs from captured. | Confirm with the provider. |
| **fee mismatch** | The fee differs from what we expected. | Finance checks the rate card. |
| **duplicate** | The same payment settled twice. | Claim it back from the provider. |

With `openpay:recon:manage` each open break has:

- **Resolve**: close it with an explanation. No money moves.
- **Match**: tie it to the payment the money belongs to. Verify with the provider first: a wrong match hides two errors behind one clean line.
- **Write off**: the money is lost and shows in the P&L. Only with finance's agreement.

## Settlements

Every settlement received: gross, fees and tax, net to the bank, line count, bank reference and status. Filter by provider.
