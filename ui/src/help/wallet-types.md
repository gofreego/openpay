# Wallet types

The rules a balance lives under: who may fund it, spend it, move it or cash it out. Every wallet is of one type.

## Choose a product first

The list shows one product's wallet types, plus the **platform** types every product can use. With **All products** selected in the sidebar, the page asks you to choose one:

![Choosing a product](wallet-types-pick.png)

![Wallet types of a product](wallet-types.png)

*What it means* summarises the capabilities. **CASH-OUT ENABLED** marks a withdrawable type.

## Capabilities

| Capability | Meaning |
|---|---|
| **Fundable** | Customers top it up with real money. |
| **Grantable** | Promotions can add value to it. |
| **Transferable** | Customers can move value to each other. |
| **Refundable to the card** | A top-up can be refunded to its source. |
| **Withdrawable** | Customers can cash out to a bank. This is a **compliance decision**: it needs `openpay:wallet_types:approve_withdrawal` and an approval reference. |

Types can also **expire** value (for example a rolling number of days) and have **limits**: maximum balance, maximum per transaction, daily load, and for withdrawable types the minimum withdrawal, approval threshold and daily withdrawal limit.

**New wallet type** (`openpay:wallet_types:write`, all-products scope) creates one. Its code, currency, scope and capabilities are permanent once created.

## A wallet type

![A wallet type](wallet-type-detail.png)

It shows every rule, and who approved cash-out if it is enabled. **Edit** changes only what stays safe once wallets exist: name, status and limits.
