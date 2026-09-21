# OpenPay — Implementation Plan

A **single-tenant, multi-product** payments, wallet, and ledger service. One company,
one set of PSP accounts, one bank account — shared by every product in the estate
(Zshala, BappaApp, …). Each product defines its own wallet types and items; the payment
machinery underneath is common.

OpenPay is **not** a payment processor — it is the orchestration, wallet, and accounting
layer that sits on top of third-party PSPs (Razorpay, Stripe, Cashfree, …) and keeps an
auditable double-entry record of every rupee that moves.

Because we collect only for our *own* products, OpenPay is a merchant, not an aggregator.
That single fact removes a large amount of regulatory surface — see Open Question Q1.

---

## Progress

**Backend track**

| Phase | Name | Status |
|------:|------|--------|
| 0 | Foundations & Platform Primitives | ◐ All but the idempotency interceptor, which needs Phase 1's first mutating endpoint |
| 1 | Products & Catalog | ☐ Not started |
| 2 | Ledger Core | ☐ Not started |
| 3 | Wallets | ☐ Not started |
| 4 | Payment Orchestration + Mock Provider | ☐ Not started |
| 5 | Real Vendor Integrations | ☐ Not started |
| 6 | Refunds, Reversals & Disputes | ☐ Not started |
| 7 | Orders & Checkout (Split Tender) | ☐ Not started |
| 8 | Settlement & Reconciliation | ☐ Not started |
| 9 | Payouts & Withdrawals | ☐ Not started |
| 10 | Hardening, Compliance & Go-Live | ☐ Not started |

**Admin UI track** (React — see Part III; runs in parallel, each phase trails its backend dependency)

| Phase | Name | Depends on | Status |
|------:|------|-----------|--------|
| U0 | Scaffold, Auth Shell & Primitives | P0 | ☐ Not started |
| U1 | Products & Catalog Screens | P1 | ☐ Not started |
| U2 | Wallet & Ledger Explorer | P2, P3 | ☐ Not started |
| U3 | Payments Console | P4, P5 | ☐ Not started |
| U4 | Refunds, Disputes & Orders | P6, P7 | ☐ Not started |
| U5 | Reconciliation & Payout Ops | P8, P9 | ☐ Not started |
| U6 | Dashboard, Polish & Hardening | P10 | ☐ Not started |

---

## Part I — Design Decisions

These are the load-bearing decisions. Everything downstream assumes them, and changing
one later is expensive. Read this section before starting any phase.

### D1. Money is `int64` minor units + ISO-4217 currency

Never `float`. An `Amount{Value int64, Currency string}` value object. `₹10.50` is
`{1050, "INR"}`. Arithmetic across different currencies must be a compile-time or
runtime error, never a silent coercion. Rounding rules (banker's vs half-up) are
declared once, in one place, and used for every fee/tax split.

### D2. Double-entry ledger is the system of record

Wallet balances are **not** a column someone increments. A balance is the consequence
of postings. Every money movement is a *journal* containing ≥2 *postings* that sum to
zero per currency.

Account types and their normal direction:

| Type | Normal | Examples |
|------|--------|----------|
| `ASSET` | Debit | `psp:razorpay:receivable`, `bank:hdfc:current` |
| `LIABILITY` | Credit | `wallet:<customer>:<type>`, `merchant:payable` |
| `INCOME` | Credit | `income:product_sales`, `income:platform_fee` |
| `EXPENSE` | Debit | `expense:psp_fees`, `expense:fx_loss` |
| `EQUITY` | Credit | `equity:opening_balance` |

**Sign convention:** a posting's raw contribution is `+amount` for DEBIT and `-amount`
for CREDIT. An account's `raw_balance = Σ postings`. The human-facing
`natural_balance = raw_balance × normal_sign(type)` where `normal_sign` is `+1` for
ASSET/EXPENSE and `-1` for LIABILITY/INCOME/EQUITY. This keeps the posting engine free
of per-type branching — it just sums integers — and pushes presentation to the edge.

**A customer wallet is a LIABILITY.** We hold money we owe the customer. If wallet
balances are assets in your model, the ledger is wrong.

### D3. The ledger is append-only

No `UPDATE` and no `DELETE` on journals or postings, ever. Mistakes are corrected by
posting a **reversal journal** that points at the original via `reverses_journal_id`.
This is what makes the system auditable and what lets you answer "what did the balance
look like at 14:03 last Tuesday".

### D4. Idempotency everywhere

- Every mutating public API requires an `Idempotency-Key` header. Store
  `(product_id, key) → request_fingerprint, response, status`. Same key + same body →
  replay the stored response. Same key + different body → `409 Conflict`.
- Every journal carries a unique `external_id` (e.g. `payment:1234:capture`). The
  unique index is what makes double-crediting a wallet *impossible*, not careful code.
- Webhooks are at-least-once and out-of-order. Dedupe on `(provider, event_id)` and
  make every handler safe to run twice.

### D5. Write path is transactional; side effects are async

Anything that touches money happens inside one database transaction. Anything that
leaves the process (webhooks out, emails, analytics) goes through a **transactional
outbox** table written in that same transaction and drained by a worker. No
"update DB then call Kafka" — that loses events.

This requires a `UnitOfWork` / `TxManager` abstraction in the repository layer, with
the transaction carried on `context.Context`. **Build this in Phase 0.** The current
flat `service.Repository` interface cannot express multi-table atomic writes and
retrofitting it after the ledger exists is a painful refactor.

### D6. Explicit state machines

Every money object has a declared set of states and legal transitions, enforced in one
place, with illegal transitions returning an error rather than silently writing.

```
Payment   CREATED → PENDING → [AUTHORIZED] → CAPTURED → SETTLED
                  ↘ FAILED | EXPIRED | CANCELLED
          CAPTURED → PARTIALLY_REFUNDED → REFUNDED
          CAPTURED → DISPUTED → DISPUTE_WON | DISPUTE_LOST
Refund    INITIATED → PENDING → PROCESSED | FAILED
Hold      ACTIVE → CAPTURED | RELEASED | EXPIRED
Order     DRAFT → PENDING_PAYMENT → PAID → FULFILLED | CANCELLED | FAILED
Payout    REQUESTED → APPROVED → PROCESSING → PAID | FAILED | REVERSED
```

### D7. Never trust the webhook, never trust the client

A webhook is a *hint* that something changed. For any state change that moves money,
fetch the authoritative status from the provider API before posting to the ledger, or
verify the signed payload cryptographically. Amounts always come from the provider's
response, never from the client's request.

### D8. `product_id` is the scoping dimension

There is no tenant. There is one platform and many products, so `product_id` carries
the scope that `tenant_id` would in a SaaS: it is on every product-owned table, applied
in the repository layer, and never optional on a product-scoped query.

The stakes differ from multi-tenancy in most places and match it in one. Mostly this is
**correct attribution**: get it wrong and revenue lands against the wrong product, or a
Zshala wallet becomes spendable in BappaApp — quiet bugs that surface at month-end, the
worst possible time to find them.

The exception is any API a product's backend calls. Because a **Customer is
platform-wide** (Part II) while their wallets are per-product, a wallet query that
forgets the caller's product leaks one product's balances into another product's app.
On that boundary, treat `product_id` exactly as strictly as a SaaS treats `tenant_id`.

Platform-level rows (PSP receivable, bank, fee expense accounts) have a `NULL`
`product_id` by design. Make that explicit in the schema rather than inventing a
sentinel product.

One consequence worth designing for deliberately: because it is all one company, a
wallet type *may* legitimately span products (a shared "MAIN" balance usable in
everything) — a real option here that a multi-tenant system could never offer. Model it
as a flag on `WalletType` (`scope: PRODUCT | PLATFORM`) from the start; retrofitting it
means re-keying wallets.

### D9. PCI scope stays at SAQ-A

Card data never touches OpenPay. Use provider-hosted checkout or provider JS SDK
tokenization. No PAN, CVV, or track data in our database, logs, or memory — ever.

### D10. Wallet behaviour is configuration, not code

Closed-loop vs. withdrawable is decided **per wallet type, at creation**. The engine has
no `COINS` or `CASHBACK` special cases — those names are just configurations of one
mechanism:

| Setting | Values | What it drives |
|---|---|---|
| `scope` | `PRODUCT` \| `PLATFORM` | where the balance is spendable (D8) |
| `fundable` | bool | may be topped up with real money (Phase 4) |
| `grantable` | bool | may be credited without a payment (promotions) |
| `withdrawable` | bool | may be cashed out (Phase 9) — **compliance-gated** |
| `transferable` | bool | customer→customer transfer allowed |
| `refundable_to_source` | bool | refunds may be routed here (Phase 6) |
| `expiry_policy` | none \| fixed \| rolling | breakage sweeper behaviour |
| `allow_negative` | bool | overdraft permitted in the posting engine |
| `limits` | min/max balance, per-txn, velocity | enforced on every operation |

Two traps this flexibility must not paper over:

**1. Purchased money and granted money are not the same money.** A rupee the customer
paid for is a real liability — potentially refundable, potentially withdrawable, and the
thing an auditor means by "customer float". A promotional coin we issued for free is
funded from `expense:promotions`, expires, and is never withdrawable. If both land in
one balance you permanently lose the ability to answer *"how much real customer money do
we hold?"* — which is the one number compliance and finance will always ask for.

Default to **separate wallet types** (`MAIN` fundable, `BONUS` grantable). Spend
ordering then becomes a tender-plan concern in Phase 7 (draw promo first,
expiring-soonest first), which is far simpler than the alternative: one mixed wallet
needs per-bucket lot tracking and FIFO consumption inside the ledger. Only take that on
if the product genuinely requires a single visible balance.

**2. A config flag cannot grant regulatory permission.** Flipping `withdrawable` on is
not a product decision — it moves the company from closed-loop into prepaid-instrument
territory (Q1). Enabling it must require an elevated permission plus a recorded
compliance approval, never an unremarkable checkbox in an admin form that someone ticks
on a Tuesday.

### D11. INR only for v1; single currency per account; no implicit FX

**v1 is INR-only.** No FX conversion journals, no gain/loss accounts, no multi-currency
checkout, and no cross-border collection — which also keeps FEMA out of scope (Q1).

Two things stay in place anyway, because they cost almost nothing now and are expensive
to add later:

- **The `currency` column stays on accounts, journals and postings.** It is one `CHAR(3)`
  per row. Stripping it would mean re-adding the dimension to the entire ledger later,
  which is the one migration you never want to run.
- **The posting engine still validates "balances per currency"**, not merely "balances".
  With one currency the two are identical, so the general form is free — and it is the
  correct invariant the day a second currency appears.

When multi-currency does arrive: cross-currency movement is two journals plus an explicit
FX conversion journal with a gain/loss account. Never an implicit conversion inside a
single journal.

Downstream simplification worth taking: the `Money` helper formats one currency (₹,
paise, 2 decimals) rather than carrying a locale matrix.

### D12. Fee policy is configurable; fee attribution and timing are not

Three questions get conflated under "who bears the fee". Only the first is a product
decision.

**1. Who bears it — configurable**, resolved per `(product, purpose)` with a platform
default. A product may absorb fees on top-ups to encourage loading, yet pass them on for
one-off purchases:

| Mode | Customer pays | Wallet credited | Used when |
|---|--:|--:|---|
| `ABSORBED` | 500 | 500 | default; fee is a cost of doing business |
| `DEDUCTED` | 500 | 490 | customer loads "whatever clears" |
| `PASSED_ON` | 510 | 500 | explicit convenience fee at checkout |

Resolution order: payment-level override (admin only, rare) → `(product, purpose)` →
product default → platform default. That is a lookup, not a rules engine — keep it so.

**2. Which account the expense lands in — not configurable.** PSP fees are always
attributed to the originating payment's product (`expense:<product>:psp_fees`). Fee load
varies enormously by payment mix (UPI ≈ 0% MDR, cards ≈ 2%, international 3%+), so a
product doing many small top-ups has a completely different cost ratio than one doing
few large payments. If attribution were a toggle, some products' costs would sit
centrally and others locally and cross-product comparison would be meaningless. Rolling
product accounts up into one company-wide cost line is trivial; splitting a central
bucket back out later is a data reconstruction job. Attribute at the finest grain, roll
up in reporting, never the reverse.

Non-transactional PSP charges — monthly minimums, chargeback handling, platform fees —
have no originating payment and stay platform-scoped.

**3. When it is recognized — not configurable.** One accounting policy per company.
Two different things happen at two different times and must not be merged:

- the **customer-facing fee** (what we charge them) is *estimated* at capture from the
  provider rate card, because `DEDUCTED` and `PASSED_ON` need a number before the
  customer is charged;
- the **actual cost** is recognized at settlement from the provider's real figures,
  because that is when it becomes a fact rather than a guess (D3).

The variance reveals itself without a dedicated account: `income:<product>:fee_recovery`
is what we charged, `expense:<product>:psp_fees` is what it cost, and the difference is
the platform's gain or loss on the estimate.

**The cost of this flexibility, stated plainly:** `DEDUCTED` and `PASSED_ON` both
require a maintained, versioned rate card per provider/method/amount band. `ABSORBED`
does not. If every product absorbs fees, skip the rate card entirely and add it the day
someone actually needs to pass a fee on.

⚠️ Surcharging is legally constrained — in India, surcharging debit cards is prohibited,
and a "convenience fee" carries its own GST treatment. `PASSED_ON` needs a compliance
check before it is enabled anywhere, not just a config change.

### D13. OpenPay records tax; it never computes it

The calling product passes **final amounts**. OpenPay owns no tax rules: no rate tables,
no place-of-supply logic, no HSN/SAC codes, no exemption handling, no invoice numbering.
That is a deliberate and valuable scope boundary — tax engines are a project of their
own — and it will be eroded by accident unless it is written down.

But "we don't compute tax" must not collapse into "tax is invisible here". A product
sending only a ₹300 total forces the ledger to book ₹300 as revenue, overstating income
by the tax component and making every finance report wrong until it is joined against
some other system.

So the API accepts the **breakdown alongside the total** — `total`, `tax_amount`,
`tax_rate`, optionally `discount` — and treats every component as authoritative
pass-through data. OpenPay stores it, posts it, and reports on it. OpenPay never derives
it.

Three rules follow:

1. **Validate arithmetic, not tax.** Assert `subtotal − discount + tax == total` and
   reject the request otherwise. That is integrity checking, not tax logic, and it
   catches a calling product's bug before it reaches an immutable ledger.
2. **Refunds carry their own breakdown.** A ₹100 partial refund of a ₹300 order holding
   ₹46 tax has a tax portion OpenPay cannot legitimately derive. Require the split on
   the refund request too. Proportional allocation exists only as a documented fallback
   when the caller omits it, and rows created that way are flagged.
3. **A missing breakdown is recorded as missing.** If a caller sends a bare total,
   book it gross and mark the journal `tax_breakdown_provided = false`, so finance can
   see exactly which revenue lacks a split rather than assuming it is tax-free.

One exception where tax genuinely originates inside OpenPay: the PSP charges **GST on
their own fee**, which arrives in the settlement report (Phase 8). That is our input
tax, not the product's output tax — split it to `asset:input_tax_credit` rather than
burying it in `expense:psp_fees`, or you overstate payment costs and lose a reclaimable
credit.

---

## Part II — Domain Model

Two distinct kinds of "user" — keeping them separate is essential:

- **Operator** — our own staff, using the admin console. Authenticated by OpenAuth,
  authorized by `x-user-perms`. Creates products and wallet types, handles ops.
- **Customer** — a person, **platform-wide**: one record across every product, holding a
  separate set of wallets per product. Never logs into OpenPay directly; their product's
  backend calls us on their behalf.

```
Platform  (= the company; PSP accounts, bank accounts, fee/income accounts
           all live here and are shared by every product)
 │
 ├── ProviderConfig               (razorpay/stripe creds — ONE set, platform-wide)
 ├── BankAccount                  (settlement destination)
 │
 └── Product                      (e.g. "Zshala", "BappaApp")
      ├── ServiceCredential       (how that product's backend authenticates to us)
      ├── WalletType              (COINS, CASHBACK, MAIN — currency, withdrawable?,
      │                            overdraft?, expiry, limits, scope PRODUCT|PLATFORM)
      ├── Item / Plan             ("Premium", price, tax class)
      ├── Order                   (what was bought; line items; tender plan)
      ├── Payment                 (intent to collect; has PaymentAttempts)
      ├── Refund / Dispute
      └── Payout                  (money out)

Customer                          (a PERSON — one record platform-wide,
 │                                 external_ref = OpenAuth user id, UNIQUE)
 └── Wallet                       (one per WalletType, backed by 1 ledger account;
                                   so one customer holds zshala:MAIN, zshala:BONUS
                                   and bappaapp:MAIN side by side)

LedgerAccount ←── LedgerPosting ──→ LedgerJournal
```

Note what moved *up* to Platform: provider credentials, bank accounts, and the fee and
receivable accounts. Every product's money flows through the same PSP and lands in the
same bank account. This is the central architectural consequence of being single-tenant,
and it is what makes Phase 8 (splitting one settlement batch across many products)
the interesting part of reconciliation.

**Customer is platform-wide, wallets are per-product.** Every product identifies people
by the same OpenAuth user id, so `customers.external_ref` is a single UNIQUE column and
identity resolution is one indexed lookup — no per-product identity mapping table, no
join on the hottest path in the system.

Three consequences that are easy to miss:

1. **A shared customer creates a real API boundary.** D8 called product scoping an
   attribution concern rather than an isolation one — that stops being true here.
   Zshala's backend asking for a customer's wallets must receive Zshala's wallets and
   nothing else. One shared customer plus a careless list query leaks BappaApp balances
   into Zshala's app. Scope wallet queries by the caller's product, always.
2. **`PLATFORM`-scoped wallet types (D10) now mean something.** A single MAIN balance
   spendable in every product becomes possible — funded through one product's checkout
   and spent in another. That is the feature, not a bug, but it does mean the top-up fee
   is attributed to the product that hosted the checkout while a different product may
   consume the balance. Accept it explicitly rather than discovering it in a P&L review.
3. **Duplicate customers still happen, and merging them moves money.** One OpenAuth id
   means one customer, so duplicates only arise when the same human holds two OpenAuth
   accounts — rarer than a free-for-all, but not rare. Merging is not an `UPDATE` of a
   foreign key: balances must be transferred by a **journal**, and the losing record
   stays as a tombstone carrying `merged_into_customer_id` so its OpenAuth id keeps
   resolving to the survivor. Audited and reversible, per D3. If OpenAuth grows its own
   account-merge flow, OpenPay follows it rather than inventing a parallel notion of
   sameness.

### Worked ledger examples

Account codes carry the product where the account is product-scoped. Platform accounts
(`psp:*`, `bank:*`, `expense:psp_fees`) have no product segment and `product_id IS NULL`.

**A. Customer tops up their Zshala wallet ₹500 (`ABSORBED` fee policy) — at capture**

| Account | Scope | Type | Dr | Cr |
|---|---|---|--:|--:|
| `psp:razorpay:receivable` | platform | ASSET | 500 | |
| `wallet:cust_9:zshala:MAIN` | product | LIABILITY | | 500 |

No fee appears yet. Per D12 the receivable is the **gross** amount the PSP owes us,
which is also the figure their reports show — so it reconciles directly. The fee becomes
a fact at settlement, in example B.

Under `PASSED_ON` the customer is charged ₹510 instead, and the extra ₹10 is credited to
`income:zshala:fee_recovery` in this same journal — estimated from the rate card, since
we must know it before charging them.

**B. Razorpay settles a batch — ₹500 (Zshala) + ₹320 (BappaApp) gross, ₹18 actual fees**

| Account | Scope | Type | Dr | Cr |
|---|---|---|--:|--:|
| `bank:hdfc:current` | platform | ASSET | 802 | |
| `expense:zshala:psp_fees` | product | EXPENSE | 10 | |
| `expense:bappaapp:psp_fees` | product | EXPENSE | 8 | |
| `psp:razorpay:receivable` | platform | ASSET | | 820 |

This is where fees become real, at their actual amounts, attributed per product (D12).
One settlement, one bank credit, many products behind it — and the split is only
possible because the matching in Phase 8 works payment-by-payment rather than on batch
totals.

**C. Customer buys Zshala "Premium" ₹300 from wallet.** Zshala passes
`total: 300, tax_amount: 46` — OpenPay posts that split, it does not derive it (D13).

| Account | Scope | Type | Dr | Cr |
|---|---|---|--:|--:|
| `wallet:cust_9:zshala:MAIN` | product | LIABILITY | 300 | |
| `income:zshala:product_sales` | product | INCOME | | 254 |
| `liability:gst_payable` | platform | LIABILITY | | 46 |

Had Zshala sent a bare `total: 300`, the full ₹300 would credit `product_sales` and the
journal would be flagged as lacking a breakdown — visibly incomplete rather than quietly
overstating revenue.

**D. Refund of C back to wallet** — a new journal linked via `reverses_journal_id`,
never an edit of C, and carrying its own tax split (D13) rather than recomputing one.

Note how the wallet liability goes *up* on top-up and *down* on spend, and how our
obligation to the customer is always visible as a single account balance. Note also that
revenue is product-scoped while tax and PSP fees are not — that split is what lets you
answer "what did Zshala earn this month" without splitting the bank account.

---

## Part III — Admin UI Design

### U-D1. Follow the house pattern: `openpay/ui/`

`openauth/admin` and `opengate/ui` both ship a React admin inside the Go service repo.
OpenPay does the same: a `ui/` directory at the repo root, package name `openpay-admin`,
same stack as `opengate/ui` — **React 19 + Vite + TypeScript + MUI v7 + emotion +
react-router-dom v7 + `@gofreego/tsutils`**. Do not introduce a different component
library or state stack; the value here is that any engineer can move between the four
admin apps without relearning anything.

The standalone `admin-ui` (BappaApp Admin) stays a shell that links out to service
consoles — it is not where OpenPay screens live.

### U-D2. OpenPay does not implement login

Auth is already solved by the platform: **OpenAuth** hosts the login page, **opengate**
validates the session and injects `x-user-id` / `x-user-perms` into downstream calls.
Those are exactly the two headers declared globally in `openpay.proto`.

So the UI uses `@gofreego/tsutils` as the other admins do — `SessionManager`,
`AuthService`, `ProtectedRoute`, `LoginCallbackPage`, redirecting to `VITE_LOGIN_URL`
when unauthenticated. **No login form, no token handling, no password anything in this
repo.** OpenPay's server trusts the gateway headers and re-checks permissions per RPC.

### U-D3. Types are generated from proto; service modules are hand-written

`api/buf.gen.yaml` already has the plugin slot for this. Enable `ts_proto` exactly as
opengate does:

```yaml
  - name: ts_proto
    path: ../ui/node_modules/.bin/protoc-gen-ts_proto
    out: ../ui/src/apis/
    opt:
      - esModuleInterop=true
      - forceLong=string
      - outputServices=grpc-js
```

Generated types land in `ui/src/apis/`; `ui/src/services/<x>Service.ts` are thin
hand-written modules calling the grpc-gateway REST endpoints through the shared
`httpClient`, importing the generated request/response types. `make setup` regenerates
Go *and* TypeScript in one step, so a proto change that breaks the UI breaks the
TypeScript build — which is the point.

### U-D4. Money arrives as a **string**, not a number

`forceLong=string` means every `int64` — including every amount in minor units — is a
TypeScript `string`. This is a feature, not an annoyance: JavaScript `number` is a
float64 and must never touch money.

- Parse with `BigInt`, never `parseFloat` / `Number()`.
- One shared `Money` util (`parse`, `format`, `add`, `sub`, `allocate`) and one
  `<Money amount currency />` component. Nothing else formats currency.
- Lint rule banning arithmetic operators on anything typed as an amount.

### U-D5. Every money-moving action carries a client-generated idempotency key

Generate a ULID when the user opens the confirm dialog, hold it for the lifetime of
that action including retries, and send it as `Idempotency-Key`. A double-click, a
flaky network, or an impatient retry then cannot double-post. Disable-on-submit is a UX
nicety; the key is the actual guarantee.

### U-D6. Two operator classes; permissions shape the UI, the server enforces it

Navigation and action buttons are gated on `x-user-perms` from the session, so operators
are not shown things they cannot do. That gating is cosmetic — never the security
boundary. Assume every hidden button can still be called directly.

The console serves **two classes of operator**, and the difference is not cosmetic:

| | Central ops | Product ops |
|---|---|---|
| Scope | whole estate | one product (or a named set) |
| Payments, orders, refunds | all | their product's only |
| Customer view | the whole person, every product | that person's wallets **in their product only** |
| Ledger | all accounts incl. platform (PSP receivable, bank) | their product's accounts only |
| Recon, settlements, provider config | yes | no — platform-level and cross-product |
| Money-moving actions | yes, with ceremony | read-mostly; adjustments stay central |
| Wallet type / fee policy config | yes | read-only |

This means **operator product scope is a server-side filter, not a UI preference.** Every
read resolves the caller's permitted product set and filters on it, exactly as strictly
as the service-credential boundary in D8. Two consequences worth stating plainly:

- Because a Customer is platform-wide, the unified cross-product customer view is
  **central-ops only**. A Zshala operator looking up a customer sees Zshala wallets.
  Showing them that person's BappaApp balance is the same leak as the API one, just
  through a different door.
- The layout's product filter is a convenience for central ops and a **fixed constraint**
  for product ops — no "All products" option, and the server would reject it anyway.

The cost of building this in U0 is a scope-aware permission hook and a filter in the
repository layer. The cost of retrofitting it is auditing every query in a console built
on the assumption that operators see everything.

### U-D7. Ledger screens are read-only; destructive actions are ceremonies

The ledger explorer has no edit affordance anywhere, matching D3. Corrections are
performed through an explicit "reverse this journal" flow. Every money-moving or
irreversible admin action requires: a typed confirmation of the amount, a **reason code
from a fixed list** plus free-text note, a preview of the resulting journal *before*
submit, and above a configurable threshold, maker-checker approval by a second operator.

### Screen inventory

| Area | Screens |
|------|---------|
| Dashboard | KPIs: success rate by provider, float held, suspense balance, unreconciled count, ledger drift status, stuck payments |
| Catalog | Products, Wallet Types, service credentials (secret shown once), Customers |
| Wallets | Customer wallet list, balance + held + available, paginated statement, adjustment flow |
| Ledger | Account browser, journal viewer (balanced postings), trial balance, drift report, reversal flow |
| Payments | List with filters (status/provider/method/amount/date), detail timeline: state transitions + raw webhook events + provider request log, manual status sync |
| Refunds | Initiate full/partial, list, detail, over-refund guard |
| Disputes | Queue, evidence upload, resolution outcome |
| Orders | List, detail with tender breakdown (wallet + gateway split), saga step status |
| Providers | Credentials (write-only), routing priority, kill-switch, health status |
| Recon | Settlement browser, break queue by classification, resolve / force-match / write-off |
| Payouts | Request queue, maker-checker approval, beneficiary verification status |
| Ops | Audit log, idempotency key lookup, outbox & DLQ viewer, job runner status |

---

## Phase 0 — Foundations & Platform Primitives

**Goal:** the plumbing every later phase assumes. No business logic. Ends with a
Postgres-backed service that can safely run multi-table transactions.

- [x] Postgres repository (`internal/repository/postgresql`) as the **only**
      implementation. The in-memory repository was removed rather than given a no-op
      `WithTx`: a repository that accepts money movement and cannot roll back is a
      trap, and "works locally, corrupts in prod" is the failure it would produce
- [x] Set up SQL migrations using `goutils/databases/migrations/sql` + the `sql-migrator`
      target already in the Makefile; establish naming/versioning convention
      (`resources/migrations/NNNNNN_name.{up,down}.sql`, golang-migrate underneath)
- [x] **`UnitOfWork` / `TxManager`** — `WithTx(ctx, fn)` carrying `*sql.Tx` on context;
      all repositories participate. Tests prove commit, multi-table rollback, rollback
      on panic, and that a nested `WithTx` joins the outer transaction
- [x] `Amount` value object (int64 minor units + currency), arithmetic with
      currency-mismatch guard, allocation/rounding helper for fee & tax splits
- [x] ID strategy: internal `bigserial` PKs + public prefixed ids (`pay_…`, `wlt_…`)
      exposed over the API; never leak sequential ids. UUIDv7 bodies, so ids sort
      chronologically and index inserts stay append-heavy
- [x] Error taxonomy (`pkg/apperrors`) → gRPC codes → HTTP; stable machine-readable
      `code` strings in every API error. Built standalone rather than on
      `goutils/customerrors`, whose `code` field means an HTTP status in some places
      and a private 1001-style constant in others. Internal errors keep their detail
      in logs and send a generic message on the wire
- [x] `idempotency_keys` table and store: atomic claim via
      `INSERT … ON CONFLICT DO NOTHING RETURNING`, so concurrent duplicates cannot both
      win; response recorded in the same transaction as the work, so a stored response
      always implies the work committed; release on failure so retries re-run; expiry
      sweep that also frees keys abandoned by a crashed process
- [ ] Idempotency **interceptor** wiring it to endpoints — lands with Phase 1's first
      mutating endpoint. Building it against no consumer would be unverifiable
- [x] Request context (`internal/appcontext`): actor (`x-user-id`), permissions,
      request id, idempotency key — populated once at each edge, read everywhere.
      Identity is mirrored into the goutils logger context so every log line carries
      it. `product_id` joins it in Phase 1 with the auth work.
      Note both edges need wiring: the HTTP path registers the service in-process, so
      gRPC interceptors never run on it and grpc-gateway needs its own middleware,
      header matcher and error handler
- [x] Observability baseline on **OpenTelemetry** — traces and metrics over OTLP, so
      the collector decides where data goes and changing backend is config, not code.
      `otelgrpc` on the gRPC server, `otelhttp` on the gateway (again two edges, since
      in-process registration bypasses interceptors), caller identity as span
      attributes, and a logger middle layer putting `traceId`/`spanId` on every log
      line so logs and traces join during an incident. Telemetry failure never blocks
      startup — a payments service does not refuse to serve because a collector is down
- [x] Health probes with the distinction that matters: `/healthz` checks **nothing**
      but the process, because a liveness probe that consults the database would fail
      on every pod during a brief outage and have the orchestrator restart the whole
      service. `/readyz` checks the database and takes the pod out of rotation without
      restarting it, with its own timeout so a hung database cannot hang the probe
- [x] Transactional **outbox** table + drainer worker. Events are written in the same
      transaction as the change they describe; the drainer claims batches with
      `FOR UPDATE SKIP LOCKED` so several can run without double-publishing, records
      per-event failures instead of stranding a batch, and drains a backlog without
      waiting on its ticker. Publishing is behind a `Publisher` interface with a log
      implementation; swap in `goutils/eventqueue` when something consumes
- [x] Background job runner (`WORKER` in `AppNames`) hosting the outbox drainer and
      the idempotency expiry sweeper; later the payment poller, hold sweeper and
      ledger invariant checker
- [x] Docker Compose for local dev: Postgres and an OTel collector that prints traces
      and metrics to its own logs (Redis/Kafka added when something needs them)

**Exit criteria:** a trivial entity can be created through the API, inside a
transaction, idempotently, with an outbox event published and consumed.

---

## Phase 1 — Products & Catalog

**Goal:** products can be registered and configured, and their backends can authenticate
to OpenPay.

- [ ] Tables: `products`, `service_credentials`, `customers`, `wallet_types`
- [ ] Two distinct auth paths, both resolved in one interceptor:
      - **Operator** (admin console) — trust `x-user-id` / `x-user-perms` injected by
        opengate; map permissions to RPC-level checks
      - **Service** (a product's backend calling us) — `key_id` + secret hashed with
        argon2id, resolving to a `product_id`; rotation and revocation
- [ ] **Operator product scope** (U-D6): resolve `x-user-perms` into an effective
      product set — all products for central ops, a named set for product ops — and
      apply it as a repository-layer filter on every read. Both auth paths therefore
      converge on the same question: *which products may this caller see?*
- [ ] Permission taxonomy covering both the verb and the scope (e.g.
      `openpay:payments:read` + product scope), plus the platform-only permissions that
      no product-scoped operator can hold: provider config, settlement/recon, platform
      ledger accounts, wallet type and fee policy writes
- [ ] Tests for the scope boundary specifically: a product-scoped operator requesting
      another product's payment, ledger account, or a shared customer's other-product
      wallets gets nothing — not a filtered-empty list that a later refactor can widen
- [ ] Product CRUD (admin API): code, display name, status, default currency
- [ ] `WalletType` config — the full capability set from D10: code, currency, scope,
      `fundable`, `grantable`, `withdrawable`, `transferable`, `refundable_to_source`,
      `expiry_policy`, `allow_negative`, limits
- [ ] Config validation rejecting incoherent combinations up front, e.g.
      `withdrawable && !fundable` (cashing out money nobody paid in), or
      `grantable && withdrawable` without an explicit override — promotional balance
      that can be converted to cash is a fraud target, not a feature
- [ ] **Compliance gate on `withdrawable`** (D10): elevated permission, a recorded
      approval row (who, when, reference to the sign-off), and a loud UI warning.
      Never a plain checkbox
- [ ] Guard rails on `WalletType` mutation: currency, scope, and the fundable/grantable
      distinction become immutable once any wallet of that type exists — enforce in code
      and state it in the API docs. Tightening limits stays allowed; loosening the
      capability flags does not
- [ ] Seed the two canonical types per product so the distinction is the default path:
      `MAIN` (fundable, closed-loop) and `BONUS` (grantable, expiring, non-withdrawable)
- [ ] `fee_policies` table (D12): mode `ABSORBED | DEDUCTED | PASSED_ON` keyed by
      `(product_id, purpose)` with a platform default row; resolution is a lookup with a
      documented precedence, not a rules engine
- [ ] `PASSED_ON` is compliance-gated the same way `withdrawable` is (surcharging
      restrictions) — elevated permission plus a recorded approval
- [ ] `customers` carries **no `product_id`** — one row per person, platform-wide, keyed
      by `external_ref` (the OpenAuth user id) UNIQUE. Upsert on it; idempotent by
      construction
- [ ] `merged_into_customer_id` nullable self-reference for the merge tombstone; every
      customer lookup follows it so a merged id keeps resolving to the survivor
- [ ] `customer_id` is **nullable** on payments and orders — a one-off guest purchase
      needs no customer record; anything holding a balance does
- [ ] **Product-scoped wallet reads** (D8): a service credential resolves to one product,
      and any wallet listing for a customer returns that product's wallets plus
      `PLATFORM`-scoped ones — never another product's. Cover this with an explicit
      test, since it is the one place a shared customer can leak data
- [ ] Customer merge (design now, implement when first needed): transfer balances **by
      journal** not by `UPDATE`, leave the losing record as a resolving tombstone rather
      than deleting it, and make the whole operation auditable and reversible
- [ ] Audit log table + interceptor: who changed what, when, from where
- [ ] Admin-facing list/filter endpoints following the existing `models/filter` pattern

**Exit criteria:** a product, two wallet types, and a customer can be created via API;
every write appears in the audit log; a revoked service credential is rejected; an
operator without the right permission is rejected.

---

## Phase 2 — Ledger Core

**Goal:** the accounting engine, fully tested, with no payment concepts in it at all.
This is the most important phase in the project. Do not rush it.

- [ ] Tables:
      - `ledger_accounts` (code UNIQUE, `product_id` NULLable for platform accounts,
        type, currency, owner, allow_negative, status)
      - `ledger_journals` (external_id UNIQUE, `product_id` NULLable, kind,
        source_kind/source_id, reverses_journal_id, memo, posted_at)
      - `ledger_postings` (journal_id, account_id, direction, amount>0, currency, seq,
        balance_after) — immutable
      - `ledger_balances` (account_id PK, raw_balance, held, version)
      - `ledger_holds` (account_id, amount, status, expires_at, external_id UNIQUE)
- [ ] Posting engine `Post(ctx, journal)`:
      - validates postings sum to zero **per currency**
      - validates every account is active and currency-matched
      - locks `ledger_balances` rows `FOR UPDATE` **in ascending account_id order**
        (deterministic order is what prevents deadlocks under concurrency)
      - enforces `allow_negative` on the resulting available balance
      - writes journal + postings + balance updates in one transaction
      - is idempotent on `external_id`
- [ ] Holds: `PlaceHold` / `CaptureHold` / `ReleaseHold`;
      `available = natural_balance − held`
- [ ] Chart-of-accounts bootstrap, split by scope:
      - **platform-level, created once**: PSP receivable per provider, bank, PSP fee
        expense, tax payable, suspense per provider
      - **product-level, created on product registration**: income, discounts,
        breakage, refunds-payable, `expense:promotions` (the funding side of every
        granted balance, per D10)
- [ ] Read APIs: account balance, paginated statement (postings with `balance_after`),
      trial balance (whole platform, and filtered per product)
- [ ] **Invariant checker job** (nightly + on-demand):
      - every journal sums to zero
      - Σ postings per account == `ledger_balances.raw_balance`
      - Σ all postings across all accounts == 0
      - alert loudly on any drift; never auto-"fix"
- [ ] Tests — this is where the effort goes:
      - property tests: random valid journals preserve all invariants
      - concurrency test: N goroutines debiting one account; assert no overdraft and
        exact final balance
      - reversal test: original + reversal nets to zero
      - deadlock test: two goroutines posting to the same account pair in opposite order

**Exit criteria:** invariant checker is green under a fuzz workload; concurrency test
passes repeatedly with `-race`; no code path can mutate a posting.

---

## Phase 3 — Wallets

**Goal:** wallets on top of the ledger, exercisable end-to-end without any PSP.

- [ ] `wallets` table; creating a wallet provisions exactly one `ledger_account`
      (LIABILITY) — unique on `(customer_id, wallet_type_id)`
- [ ] Lazy wallet creation on first reference; idempotent
- [ ] Operations: credit, debit, transfer (wallet→wallet, same currency), hold,
      capture, release — each mapping to a ledger journal with a derived `external_id`
- [ ] **Capability enforcement from `WalletType` (D10), in one place**: a single
      `assertAllowed(walletType, operation)` guard covering fundable, grantable,
      withdrawable, transferable, overdraft, limits and velocity. Every wallet operation
      routes through it — scattered `if walletType.X` checks are how these rules rot
- [ ] Grant operation (promotional credit): `Dr expense:promotions`,
      `Cr wallet` — only permitted on a `grantable` type, always reason-coded
- [ ] Wallet expiry / lapse: sweeper posting expired balance to `income:breakage`
      (purchased) or back to `expense:promotions` (granted) — the write-back differs by
      funding source, which is precisely why D10 keeps them as separate types
- [ ] `float_held` reporting query: Σ liability across **fundable** wallet types only —
      the "real customer money" figure finance and compliance will ask for. Report it
      per product, with `PLATFORM`-scoped wallets as their own bucket, since that
      balance belongs to no single product
- [ ] **Customer-level aggregate limits**, distinct from the per-wallet-type limits in
      D10. Because a customer is platform-wide, a per-person cap (total balance held,
      daily load, daily spend) spans their wallets across every product — and if a PPI
      licence is ever in scope (Q1), regulatory limits apply per *person*, not per
      wallet. Build the aggregate query now even if the caps start permissive
- [ ] APIs: get balance, list wallets for a customer, paginated wallet statement
- [ ] Admin adjustment API — permissioned, reason-coded, always a reversible journal
- [ ] Outbox events: `wallet.credited`, `wallet.debited`, `wallet.low_balance`

**Exit criteria:** create product → wallet types (`MAIN` + `BONUS`) → customer →
wallets → top-up `MAIN`, grant `BONUS` → spend from each → statements reflect both, the
trial balance is still zero, and `float_held` counts only the `MAIN` balance. Attempting
a withdrawal from `BONUS` is rejected by the capability guard.

---

## Phase 4 — Payment Orchestration + Mock Provider

**Goal:** the full collect-money pipeline, proven against a controllable fake provider
before any real vendor is involved.

- [ ] `Provider` interface:
      `CreatePayment`, `FetchPayment`, `Capture`, `Cancel`, `Refund`,
      `VerifyWebhook(headers, body) → NormalizedEvent`, `Capabilities()`
- [ ] Provider registry + per-product `provider_configs`
- [ ] **Mock provider** with programmable behaviour: success, decline, timeout,
      delayed webhook, duplicate webhook, out-of-order webhook, amount mismatch
- [ ] Tables: `payments`, `payment_attempts`, `provider_events` (raw, immutable),
      `provider_request_log`
- [ ] `POST /v1/payments` — purpose (`WALLET_TOPUP` | `ORDER` ), amount, currency,
      customer, target wallet, return URL; idempotent; returns checkout instructions
- [ ] State machine enforcement + transition audit trail on every payment
- [ ] Webhook endpoint: verify signature → persist raw event → return 200 fast →
      process asynchronously. Never do ledger work in the HTTP handler
- [ ] Webhook processor worker: dedupe on `(provider, event_id)`, fetch authoritative
      status from provider (D7), advance state machine, post ledger journal with
      `external_id = payment:<id>:capture`
- [ ] **Reconciliation poller**: payments stuck in `PENDING` past a threshold get their
      status fetched directly — webhooks *will* be missed and this is the safety net
- [ ] Expiry sweeper for abandoned payments
- [ ] Top-up flow complete: payment captured → wallet credited → `payment.succeeded`
      event emitted via outbox
- [ ] Tests: duplicate webhook credits once; out-of-order webhooks converge to the
      correct terminal state; webhook lost entirely → poller recovers

**Exit criteria:** with the mock provider, a top-up survives duplicated, delayed,
reordered, and dropped webhooks, and the wallet is credited exactly once in all cases.

---

## Phase 5 — Real Vendor Integrations

**Goal:** **Razorpay** and **Cashfree** both live, with Razorpay primary and Cashfree as
genuine redundancy.

Be clear about *why* there are two, because it changes how the phase is sequenced. Two
Indian PSPs on the same rails (UPI, cards, netbanking) teach the abstraction less than
an Indian + an international one would — the value here is **operational redundancy**,
not interface validation. With a single provider, its outage means collecting no money
at all; that is the risk being bought out.

Two consequences:

- **The mock provider still does the interface-validation job.** Keep it permanently, as
  a first-class implementation in CI. Two similar real providers can easily share a
  blind spot that the mock's programmable hostility will not.
- **Cashfree does not block go-live.** Ship Razorpay to production, then add Cashfree as
  the redundancy layer. Sequencing them keeps Phase 5 from becoming one long phase
  where nothing reaches customers.

- [ ] **Razorpay** first: orders, hosted checkout, payment verification, webhook
      signature verification, status mapping to our canonical states
- [ ] **Cashfree** second, refactoring the interface wherever it turns out to have been
      shaped around Razorpay
- [ ] Confirm both cover the needed rails, and note each one's payouts product
      (RazorpayX / Cashfree Payouts) — separate onboarding, relevant only if Q1 later
      puts Phase 9 in scope
- [ ] Keep the mock provider wired in CI as a first-class implementation, not test
      scaffolding — if an interface change only makes sense for a real provider,
      the abstraction has leaked
- [ ] Credential storage: **one credential set per provider, platform-wide** (D8) —
      envelope encryption (KMS/AES-GCM DEK); never logged, never returned by any API,
      redacted in the request log
- [ ] Payment description / statement descriptor carries the product name, so a customer
      recognises the charge and support can attribute it without a lookup
- [ ] **Rate card** per provider/method/amount band, versioned with effective dates —
      needed only if any product uses `DEDUCTED` or `PASSED_ON` (D12). Skip it entirely
      while everything is `ABSORBED`, and treat it as a stale-data risk when added:
      a rate card drifting from the provider's real pricing shows up as fee variance,
      so alert when variance exceeds a threshold rather than letting it accumulate
- [ ] Per-provider error → canonical failure-reason mapping (declined, insufficient
      funds, risk, technical) so retries and UX are provider-agnostic
- [ ] Routing: primary/secondary with health-based failover, plus rules on method and
      amount band. Record the chosen provider *and the reason* on every attempt.
      Keep it a priority list, not a rules engine — two providers do not justify one
- [ ] **Failover happens at attempt creation, never mid-payment.** Once a customer is on
      Razorpay's hosted checkout, that attempt lives and dies there; Cashfree picks up
      the *next* attempt. Design the retry UX around a fresh attempt on the healthy
      provider rather than imagining an in-flight handoff, which does not exist
- [ ] Manual provider override (kill-switch): force all new attempts to one provider
      without a deploy, for when one is degraded but not failing health checks
- [ ] Circuit breaker + timeout + bounded retry with backoff on every outbound call;
      retries must be idempotent at the provider (use their idempotency keys)
- [ ] Two of everything, tracked deliberately: credential sets, webhook endpoints and
      secrets, signature schemes, receivable accounts, suspense accounts, settlement
      report formats and cycles. Phase 8 doubles in surface because of this phase
- [ ] Provider sandbox integration tests in CI; golden-file tests for webhook parsing
      per provider
- [ ] Runbook per provider: credentials rotation, sandbox→live switch, known quirks,
      and the failover drill — including how to tell whether a provider is actually down
      versus slow

**Exit criteria:** the same top-up API call works through either provider with no
caller-visible difference; disabling Razorpay sends new attempts to Cashfree and a
top-up still completes; the mock provider still satisfies the interface with no
special-casing.

---

## Phase 6 — Refunds, Reversals & Disputes

- [ ] `refunds` table + state machine; full and partial; multiple partials per payment
      with over-refund protection enforced in the database
- [ ] Refund destination policy: back to source (PSP) vs. to wallet — per product config
- [ ] **Refund requests carry their own tax breakdown** (D13). OpenPay cannot legitimately
      derive the tax portion of a partial refund; proportional allocation is a documented
      fallback only, and refunds created that way are flagged for finance
- [ ] Ledger: refund posts a reversal-linked journal; fee refund policy configurable
      (PSP fees are often non-refundable — model that explicitly)
- [ ] Provider refund APIs + async refund webhooks (refunds are rarely synchronous)
- [ ] `disputes` / chargebacks: ingest, move funds to a `liability:disputed` holding
      account, evidence submission, win/loss resolution journals
- [ ] Reconciliation of refund/dispute fees
- [ ] Tests: refund of a wallet-funded purchase; partial refunds summing to the whole;
      refund arriving after a dispute

**Exit criteria:** a captured payment can be fully and partially refunded, the ledger
nets correctly, and a dispute lifecycle posts and reverses cleanly.

---

## Phase 7 — Orders & Checkout (Split Tender)

**Goal:** "pay for Premium" — the purchase path, including paying partly from a wallet
and partly from a card.

- [ ] `items` / `plans` (reference price, currency) and `orders` + `order_line_items`.
      The item catalogue is for description and reporting — the calling product still
      passes the amounts actually charged (D13), so a price change never silently
      rewrites what a customer was billed
- [ ] **No pricing engine** (D13). OpenPay records the breakdown the product sends —
      subtotal, discount, tax, total — validates that it adds up, rejects it if not, and
      posts each component to its own ledger account. It computes no prices and no taxes
- [ ] Flag and report orders booked without a tax breakdown, so gross-booked revenue is
      a visible exception rather than a silent assumption
- [ ] **One order belongs to exactly one product**: `orders.product_id NOT NULL`, and
      every line item validated to belong to that same product. Enforce it rather than
      assuming it — a mixed-product order is precisely the D8 failure where revenue
      lands against the wrong product, and refund routing stops being unambiguous
- [ ] Note the constraint is on the *order*, not on its funding: a `PLATFORM`-scoped
      wallet (D10) may legitimately pay for a single-product order. Tender sources span
      products; orders do not
- [ ] **Tender plan**: an order is paid by N tenders (wallet A ₹200 + gateway ₹300)
- [ ] **Tender ordering policy** (the D10 consequence): auto-build the plan by draining
      granted/expiring balances first (soonest expiry → oldest grant), then purchased
      balances, then the gateway for the remainder. Configurable per product; the
      default should minimise breakage disputes and customer surprise
- [ ] Split-tender saga with compensations:
      1. place holds on all wallet tenders
      2. create the gateway payment for the remainder
      3. on success → capture holds + post one combined journal
      4. on failure/expiry/timeout → release holds, fail the order
      5. crash at any step → the resume worker completes or compensates
- [ ] Hold expiry sweeper must never strand customer funds — test this explicitly
- [ ] Order events: `order.paid`, `order.failed` — delivered to the owning product's
      backend (internal callback or event queue; no public webhook infrastructure needed
      for in-house consumers)
- [ ] **No subscriptions.** Every payment is one-off. A time-limited purchase like
      "Premium for a month" is a single order here; the **product** owns the entitlement
      period, tracks when it lapses, and asks the customer to buy again. Resist adding a
      `subscriptions` table merely to record an expiry date — that belongs to whoever
      owns the entitlement, for the same reason tax belongs to whoever owns pricing (D13)
- [ ] Payment purpose stays `WALLET_TOPUP | ORDER`; no renewal purpose, no mandate
      storage, no dunning or retry schedules

**Exit criteria:** an order paid ₹200 wallet + ₹300 card results in one balanced
journal; killing the process mid-saga leaves no stuck holds after the sweeper runs.

---

## Phase 8 — Settlement & Reconciliation

**Goal:** prove that what the PSP says happened matches what our ledger says happened.
Until this exists, the ledger is trusted but unverified.

Being single-tenant does not simplify this phase — it complicates it in one specific
way. A single Razorpay settlement batch contains payments from *every* product, netted
into one bank credit. The matching must therefore work at the **payment level**, never
at batch totals, or per-product attribution is lost the moment two products transact on
the same day.

- [ ] `settlements` + `settlement_items`; ingest provider settlement reports
      (API or SFTP/CSV) on a schedule
- [ ] Matching engine keyed on provider transaction ref → `payment_attempts`;
      each matched item carries the product through from its payment
- [ ] Break classification: matched · missing-in-ledger · missing-at-provider ·
      amount mismatch · fee mismatch · duplicate
- [ ] Settlement journal (D12): `Dr bank`, `Cr psp:receivable` platform-scoped, with
      **actual** fees debited to `expense:<product>:psp_fees` per matched payment —
      this is the point where estimated fees become facts
- [ ] **Split GST on the PSP's fee** into `asset:input_tax_credit` rather than burying it
      in `expense:psp_fees` (D13) — it is reclaimable, and merging it overstates payment
      costs. This is the one tax OpenPay handles, because it arrives in the settlement
      report rather than from a calling product
- [ ] Fee variance report: `income:<product>:fee_recovery` vs
      `expense:<product>:psp_fees`, i.e. what we charged customers against what it
      actually cost. Persistent one-sided variance means the rate card is wrong
- [ ] Per-product settlement report: of this ₹X bank credit, which product earned what
- [ ] Unmatched items park in `suspense:<provider>` with an ops work queue — the
      suspense account balance is a KPI and should trend to zero
- [ ] Daily recon report + alerting on drift threshold and on aged breaks
- [ ] Ops APIs: resolve break, force-match with reason code, write off (permissioned,
      fully audited, always a journal)

**Exit criteria:** a day of synthetic traffic reconciles to zero breaks; an injected
mismatch is detected, classified, and alerted within one cycle.

---

## Phase 9 — Payouts & Withdrawals

> ⚠️ This phase applies only to wallet types configured `withdrawable` (D10). If every
> type is closed-loop, the customer-withdrawal half of this phase does not exist and
> only merchant/vendor payouts remain. Do not build it speculatively — and do not let a
> `withdrawable` type be enabled in production before the Q1 answer is in writing.

- [ ] `payouts` + `beneficiaries` (bank account / VPA), with verification (penny-drop)
- [ ] Payout state machine, maker-checker approval above configurable thresholds
- [ ] Ledger: `Dr wallet (liability)`, `Cr bank` — with an in-transit account between
      initiation and confirmation, because payouts fail *after* you thought they left
- [ ] Reversal handling for failed/bounced payouts (funds must return to the wallet)
- [ ] Provider payout APIs + webhooks + status poller
- [ ] Limits: per-txn, daily, velocity; risk holds; cooling period for new beneficiaries
- [ ] Inter-product accounting: no money actually moves between products (it is all one
      company, one bank account), so this is a reporting concern — per-product P&L and
      float attribution, not an internal payout

**Exit criteria:** a withdrawal debits the wallet, lands in-transit, confirms to bank,
and a forced failure returns the funds to the wallet with a clean audit trail.

---

## Phase 10 — Hardening, Compliance & Go-Live

- [ ] Security: rate limiting per service credential, request signing (HMAC) option,
      IP allowlist for admin APIs, secret rotation runbook, dependency and container
      scanning
- [ ] PII: column-level encryption for contact details; log redaction verified by test;
      data retention and deletion policy
- [ ] Reliability: a **sanity-level** load test of the posting engine (confirm it holds
      up at a multiple of expected volume, not a scaling exercise), connection pool
      tuning, graceful shutdown already in place, DR drill with documented RPO/RTO.
      No read replicas, no sharding — single Postgres is the right answer at this volume
- [ ] Ledger growth: get the indexes right so statement queries stay index-only — that
      is the whole job at this scale. **No partitioning now.** Note the one way low
      throughput still accumulates: postings are append-only forever, so the table grows
      steadily with time rather than with load. Write down a row-count trigger at which
      to revisit partitioning, and leave it alone until then
- [ ] Reporting: per-product revenue, float held, unreconciled exposure, provider
      success rates, statement exports (CSV/PDF) for finance
- [ ] Dashboards & alerts: payment success rate by provider/method, webhook lag, stuck
      payments, suspense balance, ledger drift, hold leakage
- [ ] Runbooks: stuck payment, provider outage, webhook storm, ledger drift detected,
      recon break backlog, key compromise
- [ ] API docs published from the generated swagger; an internal Go client package the
      other product backends import, so integration is not copy-pasted HTTP calls;
      staging environment pointed at PSP sandboxes
- [ ] Compliance sign-off per Q1; finance sign-off on the chart of accounts

**Exit criteria:** load test passes, DR drill completed, on-call runbooks exercised at
least once, finance can close a month from OpenPay reports alone.

---

## UI Phase U0 — Scaffold, Auth Shell & Primitives

**Goal:** an authenticated empty console with the primitives every later screen reuses.
Copy the `opengate/ui` skeleton rather than starting from `npm create vite`.

- [ ] Bootstrap `ui/` from the `opengate/ui` layout: `src/{apis,components,hooks,pages,services,types,utils}`
- [ ] `package.json` matching the house stack (React 19, MUI v7, emotion, RRD v7,
      `@gofreego/tsutils`, ts-proto in devDeps) + `generate:proto` script
- [ ] Enable the `ts_proto` plugin in `api/buf.gen.yaml` (U-D3); confirm `make setup`
      regenerates `ui/src/apis/` and that generated output is git-ignored or committed
      consistently with the other repos
- [ ] `utils/httpClient.ts` wrapping `@gofreego/tsutils` `HttpClient` with
      `VITE_API_BASE_URL`; interceptor injecting `Idempotency-Key` when supplied
- [ ] Auth shell: `ThemeProvider` → `NotificationProvider` → `BrowserRouter`, with
      `ProtectedRoute`, `/login-callback`, `NotFoundPage`, redirect to `VITE_LOGIN_URL`
- [ ] App layout: persistent sidebar nav, breadcrumb header, **product filter** (an
      "All products" default plus per-product narrowing — not a hard tenant switch,
      since ops routinely need the cross-product view), current operator + permissions
- [ ] **`Money` util + `<Money>` component** (U-D4) with BigInt arithmetic; unit tests
      covering large values, negatives, and zero-padding of minor units
- [ ] `<DataTable>` wrapper: server-side pagination (matching the backend
      `limit`/`offset` filter convention), column filters, empty/loading/error states,
      CSV export hook
- [ ] `<ConfirmAction>` ceremony component (U-D7): amount retype, reason-code select,
      journal preview slot, idempotency key lifecycle
- [ ] `<StatusChip>` driven by the canonical state machines (D6) so a status renders
      identically on every screen
- [ ] Error handling: `extractErrorMessage` + notification toasts; surface the
      backend's machine-readable `error_code` in a copyable detail line
- [ ] `usePermissions()` hook gating nav items and actions, **product-scope aware**
      (U-D6): it answers "may this operator do X, on which products?", and the layout's
      product filter is locked to that set for product-scoped operators
- [ ] Makefile, Dockerfile, nginx.conf, k8s manifests, `.env` — mirror `opengate/ui`
- [ ] CI: typecheck, lint, build, unit tests

**Exit criteria:** logging in via OpenAuth lands on an empty dashboard; a protected
route redirects when the session is cleared; `make setup` regenerates TS types and a
deliberate proto rename breaks the UI build.

---

## UI Phase U1 — Products & Catalog Screens

- [ ] Products: list, detail, create/edit; feeds the global product filter
- [ ] Wallet Types: config form rendering the full D10 capability matrix, with a plain-
      language summary of what the chosen combination means ("customers can top this up
      and spend it in Zshala; they cannot cash it out") — operators should never have to
      infer behaviour from a grid of checkboxes
- [ ] Unmistakable warnings on fields that become immutable once a wallet of that type
      exists; disable them outright once one does, with an explanatory tooltip
- [ ] `withdrawable` is a **ceremony, not a checkbox** (D10): elevated permission, an
      explicit compliance-acknowledgement step naming the approver, and a distinct
      visual treatment on any wallet type that has it enabled
- [ ] Fee policy per `(product, purpose)` (D12) with a worked example rendered live —
      "customer pays ₹510, wallet receives ₹500" beats naming a mode; `PASSED_ON`
      carries the same compliance ceremony as `withdrawable`
- [ ] Service credentials: create (**secret displayed exactly once**, copy-to-clipboard,
      never re-fetchable), rotate, revoke with confirmation
- [ ] Customers: search by `external_ref` (OpenAuth user id); detail page showing one
      person with their wallets grouped by product
- [ ] Merge ceremony for duplicate OpenAuth accounts: preview the balance-transfer
      journal before committing; merged records resolve to the survivor, not a 404
- [ ] Audit log panel embedded on each detail page (who changed what, when)

**Exit criteria:** a product with two wallet types and a customer can be created
entirely through the UI, and every write shows up in that entity's audit panel.

---

## UI Phase U2 — Wallet & Ledger Explorer

- [ ] Customer wallet list: balance, held, available — all via `<Money>`; grouped by
      product, with a cross-product total, since ops see the whole person
- [ ] Unified customer statement spanning every product — a capability the platform-wide
      customer model gives for free, and the first thing support will ask for.
      **Central ops only** (U-D6); product operators see their own product's slice
- [ ] Wallet statement: paginated postings with running `balance_after`, date/type
      filters, CSV export
- [ ] Ledger account browser: filter by type/owner/currency, drill into an account
- [ ] **Journal viewer**: postings in a debit/credit table that visibly sums to zero,
      links to source object and to any reversal
- [ ] Trial balance view (platform-wide, filterable per product); prominent banner when
      the invariant checker reports drift (never let drift be discoverable only in logs)
- [ ] Manual adjustment flow: reason code, note, **journal preview before submit**,
      permission-gated, maker-checker above threshold
- [ ] Reversal flow from a journal, with the reason ceremony

**Exit criteria:** an operator can trace a rupee from wallet statement → posting →
journal → the payment that created it, without leaving the UI or reading a log.

---

## UI Phase U3 — Payments Console

- [ ] Payments list: filters on status, provider, method, amount band, date range,
      customer; saved filter presets; CSV export
- [ ] Payment detail: canonical status, amounts, provider refs, and a **unified
      timeline** merging state transitions, received webhook events, and provider
      request/response log entries in one chronological view
- [ ] Raw webhook payload inspector (read-only, pretty-printed)
- [ ] "Sync status from provider" action for stuck payments (wraps the P4 poller)
- [ ] Stuck-payment queue view fed by the poller
- [ ] Provider config screens: credentials **write-only** (never rendered back),
      sandbox/live toggle with an unmistakable environment indicator
- [ ] Provider health / circuit-breaker panel, routing priority editor, and the manual
      kill-switch to force new attempts onto one provider — with a clear readout of
      which provider is currently taking traffic and why

**Exit criteria:** an ops person can diagnose a failed payment end-to-end from the
detail page alone — which provider, which error, which webhooks arrived, what the
ledger did.

---

## UI Phase U4 — Refunds, Disputes & Orders

- [ ] Refund initiation from a payment: full or partial, remaining-refundable shown
      live, over-refund blocked in the form and by the server
- [ ] Refund list and detail with async status tracking
- [ ] Dispute queue: incoming disputes, due dates, evidence upload, outcome recording
- [ ] Order list and detail: line items, pricing breakdown (subtotal, discount, fee,
      tax), **tender breakdown** showing wallet vs gateway split
- [ ] Saga step visualisation for split-tender orders — which holds are active,
      captured, or released; highlight stranded holds

**Exit criteria:** a partial refund of a split-tender order is executable from the UI
and the resulting journals are inspectable in the ledger explorer.

---

## UI Phase U5 — Reconciliation & Payout Ops

- [ ] Settlement browser: per provider per day, with totals vs ledger comparison
- [ ] **Break queue** grouped by classification (missing-in-ledger, missing-at-provider,
      amount mismatch, fee mismatch, duplicate), with age highlighting
- [ ] Break resolution actions: resolve, force-match with reason, write-off — all
      permission-gated ceremonies producing journals
- [ ] Suspense account trend chart; it should visibly trend to zero
- [ ] Payout queue with maker-checker approval UI (approver ≠ requester, enforced
      server-side and reflected in the UI)
- [ ] Beneficiary verification status and payout failure/reversal handling

**Exit criteria:** a day's recon can be closed from the UI, and an injected mismatch is
visible, classifiable, and resolvable with a full audit trail.

---

## UI Phase U6 — Dashboard, Polish & Hardening

- [ ] Operations dashboard: payment success rate by provider/method, float held,
      suspense balance, unreconciled count/amount, ledger drift status, webhook lag,
      stuck payment count
- [ ] Charts for volume and success-rate trends
- [ ] Global search (payment id, order id, customer ref, provider ref)
- [ ] Table virtualization for large result sets; verify statement pages stay fast
- [ ] Accessibility pass: keyboard navigation, focus management in dialogs, contrast
- [ ] Playwright e2e on the critical ceremonies: manual adjustment, refund, payout
      approval, break resolution
- [ ] Ops audit log viewer, idempotency key lookup, outbox/DLQ inspector
- [ ] Bundle/chunk tuning following the `manualChunks` pattern already used in the
      sibling admins

**Exit criteria:** an on-call engineer can answer "is payments healthy right now?" from
the dashboard in under ten seconds, and the critical flows are covered by e2e tests.

---

## Open Questions

Q2 and Q3 are resolved. Q1 is needed before Phase 9 but wants a written answer early.

1. **Regulatory posture.** Single-tenant already removes the hardest part: we collect
   for our own products, so we are a merchant, not a payment aggregator — no RBI PA
   authorization or nodal/escrow structure is implied.

   Per D10 this is now a *per-wallet-type* setting rather than a platform-wide one,
   which is the right engineering model but does **not** dissolve the question — it
   relocates it. Closed-loop types stay light. The moment any one type is set
   `withdrawable`, that is a prepaid instrument and a PPI licence is generally in scope
   in India, for the company, not for the wallet type. So the question to answer in
   writing is narrower but just as load-bearing: *are we ever enabling a withdrawable
   type, and under whose sign-off?* It also determines whether customer float may sit in
   the ordinary operating account. Needed before Phase 9, not before Phase 2.
2. ~~**Who bears PSP fees?**~~ **Resolved — see D12.** Configurable per
   `(product, purpose)` as `ABSORBED | DEDUCTED | PASSED_ON`; attribution to the
   originating product and recognition at settlement are both fixed policy. Remaining
   sub-question: is `ABSORBED` the launch default for every product? (Recommended yes —
   it needs no rate card.)
3. ~~**Is a Customer platform-wide or per-product?**~~ **Resolved: platform-wide**, keyed
   by the OpenAuth user id, which every product shares. Wallets stay per-product. See
   Part II for the three consequences: API scoping on the product boundary,
   `PLATFORM`-scoped wallet types becoming real, and customer merge moving money.
4. ~~**Vendors and currency for v1**~~ **Resolved: Razorpay + Cashfree, INR only, India
   only.** See D11 (no FX, no cross-border, FEMA out of scope) and Phase 5 — Razorpay
   primary, Cashfree for redundancy, sequenced so Cashfree does not block go-live.
5. ~~**Tax handling**~~ **Resolved: the calling product passes final amounts.** OpenPay
   records the breakdown it is given and never derives tax — see D13, including the two
   edges that follow from it (partial-refund tax portions, and GST on the PSP's own fee).
   OpenPay is also therefore **not** the invoice system of record; store an `invoice_ref`
   from the product for cross-referencing and leave numbering to whoever owns tax.
6. ~~**Expected scale**~~ **Resolved: low volume.** No sharding, no partitioning, no
   read replicas. Single Postgres, correct indexes, and a row-count trigger to revisit
   partitioning later. Note this relaxes *nothing* about correctness — the posting
   engine's lock ordering, idempotency keys, and invariant checks exist because of
   concurrency and failure, not volume, and stay exactly as specified.
7. ~~**Subscriptions in scope?**~~ **Resolved: no.** Every payment is one-off; products
   own entitlement periods (see Phase 7). A welcome side effect: no mandates and no
   card-on-file means nothing pushes PCI scope past SAQ-A (D9), and there is no dunning
   or retry-schedule machinery to build or operate.
8. ~~**Do product teams get restricted console access?**~~ **Resolved: yes.** Two
   operator classes — see U-D6 for the split and what product-scoped operators must not
   reach (provider config, recon, platform ledger accounts, and a shared customer's
   other-product wallets). Operator product scope is a server-side filter, built in U0.
9. ~~**Does an order ever span products?**~~ **Resolved: no.** One order, one product,
   enforced by `orders.product_id NOT NULL` plus line-item validation rather than left
   as an assumption. The constraint is on the order, not its funding — a `PLATFORM`
   wallet may still pay for a single-product order.

---

## Conventions

- Proto-first: define in `api/proto/openpay/v1/`, `make setup`, then implement.
  One proto file per bounded context (`wallet.proto`, `payment.proto`, `ledger.proto`).
  `make setup` regenerates Go **and** the UI's TypeScript types from the same protos.
- Layering stays as today: `service` defines the `Repository` interface it needs;
  `internal/repository/*` implements it; handlers are thin.
- UI layering mirrors `opengate/ui`: generated types in `src/apis/`, thin
  `src/services/*Service.ts` over `httpClient`, feature folders under `src/pages/`,
  shared pieces in `src/components/` and `src/hooks/`.
- Every phase ships with migrations, tests, metrics, and a runbook entry — a phase
  without tests is not complete.
- Money-touching code requires a second reviewer, backend and frontend alike.
