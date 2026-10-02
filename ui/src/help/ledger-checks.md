# Ledger checks

The invariants that prove the books are sound: **every journal balances**, and **every balance equals the sum of its postings**. They run daily and at startup.

![Ledger checks](ledger-checks.png)

- Each row is one run: when it started, the result (*OK*, *Drift* or *Failed*), what triggered it and how many findings.
- **Click a row** to expand its findings. Each says which invariant failed, for what, and the detail.
- **Run now** (`openpay:ledger:check`) runs the checks immediately. They read the whole ledger, so use it after a fix rather than on a timer.

*Drift* is serious: the ledger disagrees with itself. Raise it with engineering with the run's findings.
