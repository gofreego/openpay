# Customers

One person across every product, identified by their **OpenAuth account**. There is no browse-all list on purpose: support starts from the person who wrote in.

![Customer lookup](customers.png)

## Finding someone

Type one of these and press **Find**:

- their **OpenAuth user id**, or
- their **OpenPay customer id** (`cus_…`), or
- **any other OpenPay id** quoted in a ticket: `pay_`, `ord_`, `wlt_`, `jrn_`, `acc_`, `prd_`, `wtp_`, `dsp_`, `pot_`, `ref_`. It opens that record directly.

## The customer page

![A customer](customer-detail.png)

- **Wallets**, grouped by product, with balance, held and available amounts. Central ops also see the total *across every product*. A product operator only sees their own product's wallets.
- **Recent payments**, **Orders** and **Withdrawals** for this person.
- **History**: every recorded change to the customer.

Click a wallet to open it.

## The wallet page

![A wallet](wallet-detail.png)

- **Balance / Held / Available**: *held* is money reserved by an unfinished order; *available* is what the customer can spend.
- **Statement**: every movement with the journal behind it and the balance after. Choose a date range and **Export CSV** to send a statement.
- A red banner means the wallet is **frozen**: the customer's own operations are refused, but adjustments still apply.

### Actions

| Button | Who | What it does |
|---|---|---|
| **Grant** | `openpay:wallets:grant` | Adds promotional value, paid from the product's promotions budget. Only wallet types marked *grantable* accept it. |
| **Adjust** | `openpay:wallets:adjust`, central ops only | Corrects a balance up or down. To undo a mistaken adjustment, adjust the other way with reason *error correction*. |

Both ask you to retype the amount, pick a reason and write a note, and show the journal entry before anything moves.
