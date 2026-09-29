# OpenPay

<div align="start">
<img src="./resources/images/golang.png" alt="Go Logo" width="200"/>
</div>

OpenPay is the payments, wallet and ledger service shared by every product.
Products call it to take payments, keep customer wallets, place orders,
refund and pay out; underneath, every movement of money is a balanced
double-entry journal in PostgreSQL.

- **Ledger** — append-only journals and postings in integer minor units,
  idempotent on an external id, with invariant checks that run on a schedule.
- **Wallets** — per-product and platform wallet types with capabilities
  (fundable, grantable, transferable, withdrawable), limits, holds and expiry.
- **Payments** — provider-agnostic (a mock today; Razorpay/Cashfree next),
  webhooks treated as hints, a poller as the safety net, failover and a
  kill-switch.
- **Orders, refunds, disputes, settlement reconciliation, withdrawals,
  reports.**

The design and its decisions are in [plan.md](plan.md).

## Run it locally

```sh
docker compose up -d postgres
make setup          # generate protobuf code
make migrate        # apply resources/migrations to the dev database
go run . -env=dev   # HTTP :8085, gRPC :8086, and the background worker
```

`dev.yaml` enables the **mock provider**, whose checkout page at
`/openpay/v1/mock-provider/checkout/` lets you mark a payment paid. It must
never be enabled in production. The API explorer is at
`http://localhost:8085/openpay/v1/swagger`.

## Test

```sh
make test-integration
# or, with Postgres already up and migrated:
OPENPAY_TEST_POSTGRES=1 go test -race -p 1 ./...
```

Integration tests use a real Postgres (`openpay_test`, truncated as they go)
because row locks, `SKIP LOCKED` and constraints under contention are what
they test. `-p 1` because packages share that database.

A sanity-level load test of the posting engine is opt-in:

```sh
OPENPAY_TEST_POSTGRES=1 OPENPAY_LOAD_TEST=1 go test -run TestPostingLoad -v ./internal/wallet/
```

## Integrate a product backend

Ask central ops for a **service credential** for your product (shown once).
From Go, use the client package rather than hand-rolled calls:

```go
import (
    "github.com/gofreego/openpay/api/openpay_v1"
    "github.com/gofreego/openpay/pkg/client"
)

c, err := client.New(client.Config{Target: "openpay.internal:8086", KeyID: keyID, Secret: secret})
defer c.Close()

// Every call that moves money needs an idempotency key. Derive it from the
// action, so a retry is recognised as the same action.
ctx = client.WithIdempotencyKey(ctx, "topup:"+orderID)
err = client.Retry(ctx, 3, func(ctx context.Context) error {
    resp, err = c.CreatePayment(ctx, req)
    return err
})
switch client.Code(err) {
case client.WalletOperationDenied, client.InsufficientBalance:
    // tell the customer
}
```

From anything else, use HTTP: `Authorization: Bearer <key_id>.<secret>`, an
`Idempotency-Key` header on mutations, and branch on the `code` field of
error bodies, never on the message. The full surface is in the swagger UI.

A credential belongs to exactly one product. It sees that product's
customers' wallets (and platform wallets), never another product's.

## Operate

- [On-call runbooks](docs/runbooks/README.md) — stuck payments, provider
  outages, webhook storms, ledger drift, recon breaks, key compromise.
- Health: `GET /healthz` (process), `GET /readyz` (database).
- Encryption keys for bank account numbers (`Service.Encryption`) and all
  provider secrets belong in a secret store, never in a committed file.
