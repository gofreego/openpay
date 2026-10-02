# Withdrawals

Cash-outs from withdrawable wallets to customers' banks. Above a wallet type's threshold, **a second person approves** before anything is sent.

![The withdrawals list](withdrawals.png)

- The list opens on **Pending approval**: the queue to work through. Change **Status** to see paid, rejected or failed ones.
- This page covers every product; it ignores the sidebar product filter.

## The withdrawal page

![A withdrawal](withdrawal-detail.png)

It shows the amount, the wallet, the destination, whether approval was needed, who requested it and who decided.

### Approve or reject

Users with `openpay:withdrawals:approve` (all-products scope) see **Approve** and **Reject** while it is pending.

- **Approve** sends the money to the bank at once. Check the destination and the name at the bank on the customer's page first.
- **Reject** puts the money back in the wallet; the customer can ask again.

Both need a note. **You cannot approve your own request**: the page tells you so, and the server refuses it anyway.
