# Reports

Read straight off the ledger and payment records, so they agree with every statement.

![Reports](reports.png)

Choose **From** and **To** (inclusive) at the top. The sidebar **Product** filter also applies.

## Income and expense by product

A P&L per product: each income and expense account with its amount for the period, and the product's income, expense and net. For example:

- `income:<product>:product_sales`: revenue from orders.
- `income:<product>:breakage`: expired wallet value.
- `expense:<product>:promotions`: value granted to customers.
- `expense:<product>:psp_fees`: provider fees.
- `expense:<product>:chargebacks`: disputes lost.

## Provider success

Per provider for the period: success rate, attempts, captured, failed, expired, captured amount and the most common failure codes. Use it to compare providers or spot a bad day.
