# Ledger accounts

The double-entry ledger behind every balance. **Read-only**: every balance is the sum of its postings, and nothing here can be edited.

![Ledger accounts](ledger-accounts.png)

## Filters

- **Code starts with**: for example `income:`, `wallet`, `psp:mock:`.
- **Type**: asset, liability, income, expense or equity.
- **Platform accounts only** (central ops): accounts that belong to no product, such as provider receivables, suspense and bank accounts.
- The sidebar **Product** filter also applies.

Account codes read as *kind:owner:purpose*, for example `income:zshala:product_sales`. *Balance* is coloured by sign.

If the last ledger check found a problem, a red banner appears above the table with a link to the details.

## An account

![A ledger account](ledger-account-detail.png)

Click an account to see its balance and statement. Each posting links to its **journal**, the balanced set of entries one money movement made.
