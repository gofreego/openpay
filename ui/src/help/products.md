# Products

Our own apps (zshala, bappaapp…). Every wallet, order and rupee of revenue belongs to one product.

![Products](products.png)

- Search by **code or name**, filter by **Status**.
- **New product** (`openpay:products:write`, all-products scope) registers one. The **code** is lowercase and permanent; it appears in account codes and permissions.

## A product

![A product](product-detail.png)

- **Order refunds go to**: *store credit (wallet)* or *back to how it was paid*.
- **Wallet types** this product offers. **Manage** opens them.
- **Service credentials**: the keys the product's backend uses to call OpenPay.
- **History**: every change.

### Actions

| Button | Who | What it does |
|---|---|---|
| **Edit** | `openpay:products:write` | Rename, change the refund destination, or **suspend** the product. Suspending stops all its traffic, including live credentials. |
| **Issue credential** | `openpay:credentials:write` | Creates a key for the product's backend. **The secret is shown once**; copy it then. If it is lost, issue a new one and revoke the old. |
| **Revoke** | `openpay:credentials:write` | Stops a credential working immediately. |
