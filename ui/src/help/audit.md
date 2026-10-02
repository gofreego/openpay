# Audit log

Every recorded change: **who** did it, **what** changed, and the state **before and after**.

![The audit log](audit.png)

- **Who**: a user id, or a service credential (`scr_…`) when a product's backend made the change.
- **Resource id**: every change to one record, for example a wallet or a product.
- Click **Changes** on a row to see the before and after.
- **Request** ties the change to the server logs, for engineering.

Every detail page also has a **History** section showing the same entries for that one record.
