# Orders

Purchases made in a product, paid from wallets, the card, or both.

![The orders list](orders.png)

Filter by **Status**; the sidebar **Product** filter also applies. *Product ref* is the product's own order number. *On card* is the part paid through the payment provider; the rest came from wallets.

## The order page

![An order](order-detail.png)

- **Summary**: subtotal, discount, tax, total, refunded so far, and the card payment if there was one.
- **How it was paid**: each tender (wallet or card/UPI), its amount and step. A wallet tender is *held* while the order is unpaid, then *captured* or *released*.
- **Lines**: the items, quantities and prices.
- **Refunds** against this order, and **History**.

### Warnings

- **A wallet hold is still active on a finished order**: customer money is locked. The hold sweeper should release it; if it stays, raise it.
- **Booked without a tax breakdown**: the product sent no tax split, so the whole amount was treated as revenue.

### Refund order

The **Refund** button returns money for the order. Where it goes depends on the product's *Order refunds go to* setting: store credit in a wallet, or back the way it was paid. You can give the subtotal, discount and tax split yourself, or let OpenPay split it in proportion.
