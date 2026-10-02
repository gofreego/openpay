# Disputes

Chargebacks: a customer asked their bank to reverse a payment. Evidence must reach the card network **before its due date**, or the dispute is lost by default.

![The disputes list](disputes.png)

- The list opens on **Open** disputes; change **Status** to see the rest.
- **Evidence due** turns red when less than three days remain.
- This page covers every product; it ignores the sidebar product filter.

## The dispute page

![A dispute](dispute-detail.png)

When a dispute opens, OpenPay sets the amount aside:

- **Held from the wallet**: taken from the customer's balance while the dispute runs.
- **Held from unapplied money**: taken from money not yet applied.
- **Not recoverable**: becomes an expense if the dispute is lost.

### Submit evidence

Users with `openpay:disputes:manage` (all-products scope) see **Submit evidence** while the dispute is open. Write what proves the payment was genuine: delivery proof, usage logs, the customer's own confirmation. It speaks for the company to the card network and **can be submitted once**.
