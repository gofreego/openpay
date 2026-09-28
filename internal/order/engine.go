// Package order records purchases and settles them — from wallets, from a
// card, or both at once (split tender).
//
// OpenPay prices nothing and computes no tax (plan.md D13). The product sends
// the amounts; this package checks they add up, records them, and books each
// component to its own ledger account.
package order

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/internal/payment"
	"github.com/gofreego/openpay/internal/wallet"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"

	"github.com/gofreego/goutils/logger"
)

type Repository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	WithSavepoint(ctx context.Context, fn func(ctx context.Context) error) error

	CreateOrder(ctx context.Context, o *dao.Order) error
	UpdateOrder(ctx context.Context, o *dao.Order) error
	LockOrder(ctx context.Context, id int64) (*dao.Order, error)
	GetOrderByExternalRef(ctx context.Context, productID int64, externalRef string) (*dao.Order, error)
	CreateOrderLine(ctx context.Context, l *dao.OrderLine) error
	CreateOrderTender(ctx context.Context, t *dao.OrderTender) error
	UpdateOrderTender(ctx context.Context, t *dao.OrderTender) error
	ListOrderTenders(ctx context.Context, orderID int64) ([]*dao.OrderTender, error)
	GetOrderPayment(ctx context.Context, orderID int64) (*dao.Payment, error)
	GetItemByPublicID(ctx context.Context, publicID string) (*dao.Item, error)

	GetProductByID(ctx context.Context, id int64) (*dao.Product, error)
	GetWalletTypeByID(ctx context.Context, id int64) (*dao.WalletType, error)
	GetWalletByPublicID(ctx context.Context, publicID string) (*dao.Wallet, error)
	ListCustomerWallets(ctx context.Context, customerID int64, scope *filter.ProductScope, includePlatform bool) ([]*dao.Wallet, error)
	GetLedgerAccountByCode(ctx context.Context, code string) (*dao.LedgerAccount, error)
	GetBalance(ctx context.Context, accountID int64) (*dao.Balance, error)
	GetHoldByExternalID(ctx context.Context, externalID string) (*dao.Hold, error)
	PostJournal(ctx context.Context, journal *dao.Journal) error
	CaptureHolds(ctx context.Context, holdExternalIDs []string, journal *dao.Journal) ([]*dao.Hold, bool, error)
	SaveOutboxEvent(ctx context.Context, event *dao.OutboxEvent) error

	CreateOrderRefund(ctx context.Context, x *dao.OrderRefund) error
	CreateOrderRefundPart(ctx context.Context, p *dao.OrderRefundPart) error
	ListWalletTypes(ctx context.Context, productID int64) ([]*dao.WalletType, error)
	GetCustomerByPublicID(ctx context.Context, publicID string) (*dao.Customer, error)
	GetJournalByExternalID(ctx context.Context, externalID string) (*dao.Journal, error)
}

type Config struct {
	// TTL is how long a customer has to pay an order's card share.
	TTL time.Duration `yaml:"TTL"`
	// HoldGrace keeps wallet holds alive past the order's expiry, so the
	// order sweeper — which asks the provider before failing an order — runs
	// before the hold sweeper could release money a late capture still needs.
	HoldGrace time.Duration `yaml:"HoldGrace"`
}

func (c *Config) WithDefaults() {
	if c.TTL <= 0 {
		c.TTL = 30 * time.Minute
	}
	if c.HoldGrace <= 0 {
		c.HoldGrace = time.Hour
	}
}

type Engine struct {
	repo     Repository
	wallets  *wallet.Engine
	payments *payment.Engine
	cfg      Config
	now      func() time.Time
}

// New builds the order engine and connects it to the payment engine, which
// then reports order payment captures and failures to it.
func New(repo Repository, wallets *wallet.Engine, payments *payment.Engine, cfg Config) *Engine {
	cfg.WithDefaults()
	e := &Engine{repo: repo, wallets: wallets, payments: payments, cfg: cfg, now: time.Now}
	payments.SetOrderHook(e)
	return e
}

type Line struct {
	ItemPublicID string
	Description  string
	Quantity     int
	UnitAmount   int64
}

type WalletTender struct {
	Wallet *dao.Wallet
	Amount int64
}

type CreateRequest struct {
	ProductID   int64
	Customer    *dao.Customer // nil for a guest
	ExternalRef string
	InvoiceRef  string
	Currency    string

	Subtotal, Discount, Tax, Total int64
	TaxRate                        string
	// BreakdownProvided is false for a bare total, booked gross and flagged.
	BreakdownProvided bool

	Lines []Line

	// Tenders are explicit wallet shares. AutoTender instead draws from the
	// customer's wallets by the default policy. Either way, a card pays the rest.
	Tenders    []WalletTender
	AutoTender bool

	Description string
	ReturnURL   string
}

type Result struct {
	Order   *dao.Order
	Tenders []*dao.OrderTender
	Payment *dao.Payment
	Attempt *dao.PaymentAttempt
}

// Create records an order and starts paying for it, in the caller's
// transaction.
//
//   - Wallets alone cover it: one journal settles it now.
//   - A card share remains: the wallet shares are held — reserved, not yet
//     spent — and a gateway payment is opened for the rest. Its capture
//     settles everything in one journal; its failure releases the holds.
//     Both happen in the payment's transaction, so no crash can leave an
//     order and its payment disagreeing, or a hold stranded.
func (e *Engine) Create(ctx context.Context, req CreateRequest) (*Result, error) {
	if err := validateAmounts(req); err != nil {
		return nil, err
	}
	if existing, err := e.repo.GetOrderByExternalRef(ctx, req.ProductID, req.ExternalRef); err == nil {
		if existing.Total != req.Total || existing.Currency != req.Currency {
			return nil, apperrors.New(apperrors.AlreadyExists,
				"order %q already exists with a different total", req.ExternalRef)
		}
		return e.result(ctx, existing)
	} else if !apperrors.Is(err, apperrors.NotFound) {
		return nil, err
	}

	var result *Result
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		tenders, err := e.planTenders(ctx, req)
		if err != nil {
			return err
		}
		var walletShare int64
		for _, t := range tenders {
			walletShare += t.Amount
		}

		o := &dao.Order{
			PublicID: ids.New(ids.Order), ProductID: req.ProductID, ExternalRef: req.ExternalRef,
			InvoiceRef: req.InvoiceRef, Currency: req.Currency,
			Subtotal: req.Subtotal, Discount: req.Discount, Tax: req.Tax, Total: req.Total,
			TaxRate: req.TaxRate, TaxBreakdownProvided: req.BreakdownProvided,
			Status: dao.OrderPendingPayment, GatewayAmount: req.Total - walletShare,
			ExpiresAt: e.now().Add(e.cfg.TTL),
		}
		if req.Customer != nil {
			o.CustomerID, o.CustomerPublicID = &req.Customer.ID, &req.Customer.PublicID
		}
		if err := e.repo.CreateOrder(ctx, o); err != nil {
			return err
		}
		if err := e.recordLines(ctx, o, req.Lines); err != nil {
			return err
		}

		if o.GatewayAmount == 0 {
			if err := e.settleFromWallets(ctx, o, tenders); err != nil {
				return err
			}
		} else if err := e.holdAndCharge(ctx, o, tenders, req); err != nil {
			return err
		}

		// Re-read: opening the card payment may already have failed the order
		// through the payment hook, which worked on its own copy.
		current, err := e.repo.LockOrder(ctx, o.ID)
		if err != nil {
			return err
		}
		result, err = e.result(ctx, current)
		return err
	})
	return result, err
}

// validateAmounts is integrity checking, not tax logic (D13): the parts
// must add up. The database checks the same; this says why.
func validateAmounts(req CreateRequest) error {
	if req.Total <= 0 {
		return apperrors.New(apperrors.InvalidArgument, "total must be positive")
	}
	if req.Subtotal < 0 || req.Discount < 0 || req.Tax < 0 || req.Discount > req.Subtotal {
		return apperrors.New(apperrors.InvalidArgument, "subtotal, discount and tax must be non-negative, and discount at most the subtotal")
	}
	if req.Subtotal-req.Discount+req.Tax != req.Total {
		return apperrors.New(apperrors.InvalidArgument,
			"subtotal %d - discount %d + tax %d = %d, not the total %d",
			req.Subtotal, req.Discount, req.Tax, req.Subtotal-req.Discount+req.Tax, req.Total)
	}
	if len(req.Lines) > 0 {
		var sum int64
		for _, l := range req.Lines {
			if l.Quantity <= 0 || l.UnitAmount < 0 {
				return apperrors.New(apperrors.InvalidArgument, "line quantities must be positive and amounts non-negative")
			}
			sum += int64(l.Quantity) * l.UnitAmount
		}
		if sum != req.Subtotal {
			return apperrors.New(apperrors.InvalidArgument, "line items sum to %d, not the subtotal %d", sum, req.Subtotal)
		}
	}
	if req.Customer == nil && (len(req.Tenders) > 0 || req.AutoTender) {
		return apperrors.New(apperrors.InvalidArgument, "a guest order has no wallets to pay from")
	}
	return nil
}

// recordLines stores the line items, each checked to belong to the order's
// product: a line from another product is how revenue lands in the wrong place.
func (e *Engine) recordLines(ctx context.Context, o *dao.Order, lines []Line) error {
	for _, l := range lines {
		line := &dao.OrderLine{OrderID: o.ID, Description: l.Description, Quantity: l.Quantity,
			UnitAmount: l.UnitAmount, Amount: int64(l.Quantity) * l.UnitAmount}
		if l.ItemPublicID != "" {
			item, err := e.repo.GetItemByPublicID(ctx, l.ItemPublicID)
			if err != nil {
				return err
			}
			if item.ProductID != o.ProductID {
				return apperrors.New(apperrors.InvalidArgument, "item %s belongs to another product", l.ItemPublicID)
			}
			if item.Currency != o.Currency {
				return apperrors.New(apperrors.InvalidArgument, "item %s is priced in %s, the order is in %s",
					l.ItemPublicID, item.Currency, o.Currency)
			}
			line.ItemID = &item.ID
			if line.Description == "" {
				line.Description = item.Name
			}
		}
		if err := e.repo.CreateOrderLine(ctx, line); err != nil {
			return err
		}
	}
	return nil
}

// planTenders validates explicit wallet shares, or draws them by policy.
func (e *Engine) planTenders(ctx context.Context, req CreateRequest) ([]WalletTender, error) {
	if req.AutoTender {
		return e.autoTender(ctx, req)
	}
	var sum int64
	seen := map[int64]bool{}
	for _, t := range req.Tenders {
		if err := e.checkTender(ctx, req, t.Wallet, t.Amount); err != nil {
			return nil, err
		}
		if seen[t.Wallet.ID] {
			return nil, apperrors.New(apperrors.InvalidArgument, "wallet %s appears twice", t.Wallet.PublicID)
		}
		seen[t.Wallet.ID] = true
		sum += t.Amount
	}
	if sum > req.Total {
		return nil, apperrors.New(apperrors.InvalidArgument, "wallet shares %d exceed the total %d", sum, req.Total)
	}
	return req.Tenders, nil
}

// checkTender: the wallet is the customer's, spendable in this product, in
// the order's currency, and allowed to spend.
func (e *Engine) checkTender(ctx context.Context, req CreateRequest, w *dao.Wallet, amount int64) error {
	if amount <= 0 {
		return apperrors.New(apperrors.InvalidArgument, "a wallet share must be positive")
	}
	if w.CustomerID != req.Customer.ID {
		return apperrors.New(apperrors.InvalidArgument, "wallet %s is not this customer's", w.PublicID)
	}
	// A platform wallet may pay for any product's order; a product wallet only
	// for its own. The order is single-product; its funding need not be.
	if w.ProductID != nil && *w.ProductID != req.ProductID {
		return apperrors.New(apperrors.NotFound, "wallet %q not found", w.PublicID)
	}
	walletType, err := e.repo.GetWalletTypeByID(ctx, w.WalletTypeID)
	if err != nil {
		return err
	}
	if walletType.Currency != req.Currency {
		return apperrors.New(apperrors.InvalidArgument, "wallet %s holds %s, the order is in %s", w.PublicID, walletType.Currency, req.Currency)
	}
	return e.wallets.CheckSpend(ctx, w, amount)
}

// autoTender is the default tender policy (plan.md D10's consequence): spend
// granted value before purchased, and expiring before permanent, so a
// customer never loses promotional credit to expiry while their own money
// pays. The card pays whatever the wallets cannot.
//
// Rolling expiry has no per-credit date, so "expiring soonest" is
// approximated by "has an expiry at all"; per-credit ordering arrives with
// fixed-expiry lots.
func (e *Engine) autoTender(ctx context.Context, req CreateRequest) ([]WalletTender, error) {
	wallets, err := e.repo.ListCustomerWallets(ctx, req.Customer.ID, filter.OnlyProducts(req.ProductID), true)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		w         *dao.Wallet
		available int64
		rank      int
	}
	var candidates []candidate
	for _, w := range wallets {
		if w.Status != dao.WalletActive {
			continue
		}
		walletType, err := e.repo.GetWalletTypeByID(ctx, w.WalletTypeID)
		if err != nil {
			return nil, err
		}
		if walletType.Currency != req.Currency {
			continue
		}
		balance, err := e.repo.GetBalance(ctx, w.LedgerAccountID)
		if err != nil {
			return nil, err
		}
		available := balance.Available(dao.AccountLiability)
		if available <= 0 {
			continue
		}
		rank := 2
		if walletType.ExpiryPolicy != dao.ExpiryNone {
			rank = 1
		}
		if walletType.Grantable && !walletType.Fundable {
			rank = 0
		}
		candidates = append(candidates, candidate{w, available, rank})
	}
	slices.SortStableFunc(candidates, func(a, b candidate) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		return int(a.w.ID - b.w.ID)
	})

	var tenders []WalletTender
	remaining := req.Total
	for _, c := range candidates {
		if remaining == 0 {
			break
		}
		take := min(c.available, remaining)
		if err := e.wallets.CheckSpend(ctx, c.w, take); err != nil {
			continue // e.g. above this wallet type's per-transaction limit
		}
		tenders = append(tenders, WalletTender{Wallet: c.w, Amount: take})
		remaining -= take
	}
	return tenders, nil
}

// settleFromWallets pays an order entirely from wallets, in one journal.
func (e *Engine) settleFromWallets(ctx context.Context, o *dao.Order, tenders []WalletTender) error {
	var rows []*dao.OrderTender
	var wallets []*dao.Wallet
	for _, t := range tenders {
		row := &dao.OrderTender{OrderID: o.ID, Kind: dao.TenderWallet, WalletID: &t.Wallet.ID,
			Amount: t.Amount, Status: dao.TenderCaptured, WalletPublicID: &t.Wallet.PublicID}
		if err := e.repo.CreateOrderTender(ctx, row); err != nil {
			return err
		}
		rows = append(rows, row)
		wallets = append(wallets, t.Wallet)
	}
	journal, err := e.settlementJournal(ctx, o, rows, "")
	if err != nil {
		return err
	}
	if err := e.repo.PostJournal(ctx, journal); err != nil {
		return err
	}
	if err := e.wallets.EmitMovements(ctx, journal, wallets); err != nil {
		return err
	}
	return e.markPaid(ctx, o)
}

// holdAndCharge reserves the wallet shares and opens the card payment.
func (e *Engine) holdAndCharge(ctx context.Context, o *dao.Order, tenders []WalletTender, req CreateRequest) error {
	for _, t := range tenders {
		if _, err := e.wallets.Hold(ctx, wallet.HoldRequest{
			Wallet: t.Wallet, Amount: t.Amount, Reference: o.PublicID,
			ExpiresAt: o.ExpiresAt.Add(e.cfg.HoldGrace),
		}); err != nil {
			return err
		}
		if err := e.repo.CreateOrderTender(ctx, &dao.OrderTender{OrderID: o.ID, Kind: dao.TenderWallet,
			WalletID: &t.Wallet.ID, Amount: t.Amount, Status: dao.TenderHeld}); err != nil {
			return err
		}
	}
	if err := e.repo.CreateOrderTender(ctx, &dao.OrderTender{OrderID: o.ID, Kind: dao.TenderGateway,
		Amount: o.GatewayAmount, Status: dao.TenderHeld}); err != nil {
		return err
	}
	_, _, err := e.payments.CreateOrderPayment(ctx, payment.OrderPaymentRequest{
		ProductID: o.ProductID, Customer: req.Customer, OrderID: o.ID,
		Amount: o.GatewayAmount, Currency: o.Currency,
		Description: req.Description, ReturnURL: req.ReturnURL, ExpiresAt: o.ExpiresAt,
	})
	return err
}

// settlementJournal is the one journal that pays for an order (plan.md
// Part II, example C):
//
//	Dr each wallet share            (the customer's balance goes down)
//	Dr the PSP receivable           (the card share, if any)
//	Dr expense:<product>:discounts  (if any)
//	Cr income:<product>:product_sales   subtotal
//	Cr liability:gst_payable            tax
//
// Revenue is the product's, tax is the platform's liability, and every
// component the product sent lands in its own account.
func (e *Engine) settlementJournal(ctx context.Context, o *dao.Order, tenders []*dao.OrderTender, providerName string) (*dao.Journal, error) {
	product, err := e.repo.GetProductByID(ctx, o.ProductID)
	if err != nil {
		return nil, err
	}
	account := func(code string) (int64, error) {
		a, err := e.repo.GetLedgerAccountByCode(ctx, code)
		if err != nil {
			return 0, err
		}
		return a.ID, nil
	}

	var postings []*dao.Posting
	add := func(accountID int64, direction dao.Direction, amount int64) {
		if amount > 0 {
			postings = append(postings, &dao.Posting{AccountID: accountID, Direction: direction, Amount: amount, Currency: o.Currency})
		}
	}
	for _, t := range tenders {
		switch t.Kind {
		case dao.TenderWallet:
			w, err := e.repo.GetWalletByPublicID(ctx, *t.WalletPublicID)
			if err != nil {
				return nil, err
			}
			add(w.LedgerAccountID, dao.Debit, t.Amount)
		case dao.TenderGateway:
			id, err := account(ledger.PSPReceivable(providerName))
			if err != nil {
				return nil, err
			}
			add(id, dao.Debit, t.Amount)
		}
	}
	for _, leg := range []struct {
		code      string
		direction dao.Direction
		amount    int64
	}{
		{ledger.ProductDiscounts(product.Code), dao.Debit, o.Discount},
		{ledger.ProductSales(product.Code), dao.Credit, o.Subtotal},
		{ledger.GSTPayable, dao.Credit, o.Tax},
	} {
		if leg.amount == 0 {
			continue
		}
		id, err := account(leg.code)
		if err != nil {
			return nil, err
		}
		add(id, leg.direction, leg.amount)
	}

	source := "order"
	memo := "order " + o.ExternalRef
	if !o.TaxBreakdownProvided {
		memo += " (no tax breakdown provided: booked gross)"
	}
	return &dao.Journal{
		PublicID: ids.New(ids.LedgerJournal), ExternalID: "order:" + o.PublicID + ":paid",
		Kind: dao.JournalPurchase, ProductID: &o.ProductID, SourceKind: &source, SourceID: &o.PublicID,
		Memo: memo, Postings: postings,
	}, nil
}

// CaptureOrder settles an order when its card share is captured: the held
// wallet shares and the card money in one journal. It implements
// payment.OrderHook and runs in the payment's transaction.
//
// If the order can no longer take the money — it already failed, or a
// wallet share has gone (its hold expired and the balance was spent) — it
// fails the order, releases what it held, and reports applied false so the
// payment engine owes the card money back.
func (e *Engine) CaptureOrder(ctx context.Context, p *dao.Payment, providerName string) (bool, error) {
	o, err := e.repo.LockOrder(ctx, *p.OrderID)
	if err != nil {
		return false, err
	}
	if o.Status != dao.OrderPendingPayment {
		return false, nil
	}
	if p.CapturedAmount == nil || *p.CapturedAmount != o.GatewayAmount {
		return false, e.fail(ctx, o, "card share captured for a different amount")
	}

	tenders, err := e.repo.ListOrderTenders(ctx, o.ID)
	if err != nil {
		return false, err
	}
	var activeHolds []string
	var wallets []*dao.Wallet
	for _, t := range tenders {
		if t.Kind != dao.TenderWallet {
			continue
		}
		w, err := e.repo.GetWalletByPublicID(ctx, *t.WalletPublicID)
		if err != nil {
			return false, err
		}
		wallets = append(wallets, w)
		holdID := wallet.HoldExternalID(w, o.PublicID)
		if hold, err := e.repo.GetHoldByExternalID(ctx, holdID); err == nil && hold.Status == dao.HoldActive {
			activeHolds = append(activeHolds, holdID)
		}
	}

	journal, err := e.settlementJournal(ctx, o, tenders, providerName)
	if err != nil {
		return false, err
	}
	settle := e.repo.WithSavepoint(ctx, func(ctx context.Context) error {
		if len(activeHolds) == 0 {
			return e.repo.PostJournal(ctx, journal)
		}
		_, _, err := e.repo.CaptureHolds(ctx, activeHolds, journal)
		return err
	})
	if apperrors.Is(settle, apperrors.InsufficientBalance) {
		return false, e.fail(ctx, o, "a wallet share was no longer available when the card payment completed")
	}
	if settle != nil {
		return false, settle
	}

	for _, t := range tenders {
		t.Status = dao.TenderCaptured
		if err := e.repo.UpdateOrderTender(ctx, t); err != nil {
			return false, err
		}
	}
	if err := e.wallets.EmitMovements(ctx, journal, wallets); err != nil {
		return false, err
	}
	return true, e.markPaid(ctx, o)
}

// PaymentEnded fails an order whose card payment failed, expired or was
// cancelled, releasing its holds. It implements payment.OrderHook.
func (e *Engine) PaymentEnded(ctx context.Context, p *dao.Payment) error {
	o, err := e.repo.LockOrder(ctx, *p.OrderID)
	if err != nil {
		return err
	}
	if o.Status != dao.OrderPendingPayment {
		return nil
	}
	return e.fail(ctx, o, "card payment "+string(p.Status))
}

// fail marks an order failed and lets go of every hold it has.
func (e *Engine) fail(ctx context.Context, o *dao.Order, reason string) error {
	tenders, err := e.repo.ListOrderTenders(ctx, o.ID)
	if err != nil {
		return err
	}
	for _, t := range tenders {
		if t.Status != dao.TenderHeld {
			continue
		}
		if t.Kind == dao.TenderWallet {
			w, err := e.repo.GetWalletByPublicID(ctx, *t.WalletPublicID)
			if err != nil {
				return err
			}
			// Already released or expired is fine: either way nothing is held.
			if _, err := e.wallets.ReleaseHold(ctx, w, o.PublicID); err != nil && !apperrors.Is(err, apperrors.FailedPrecondition) {
				return err
			}
		}
		t.Status = dao.TenderReleased
		if err := e.repo.UpdateOrderTender(ctx, t); err != nil {
			return err
		}
	}
	o.Status, o.FailureReason = dao.OrderFailed, &reason
	if err := e.repo.UpdateOrder(ctx, o); err != nil {
		return err
	}
	logger.Info(ctx, "order %s failed: %s", o.PublicID, reason)
	return e.emit(ctx, o, TopicOrderFailed)
}

func (e *Engine) markPaid(ctx context.Context, o *dao.Order) error {
	now := e.now()
	o.Status, o.PaidAt = dao.OrderPaid, &now
	if err := e.repo.UpdateOrder(ctx, o); err != nil {
		return err
	}
	return e.emit(ctx, o, TopicOrderPaid)
}

// Expire resolves an order still awaiting payment past its expiry, by
// resolving its payment — which asks the provider first, so a customer who
// paid at the last moment gets their order rather than a failure.
func (e *Engine) Expire(ctx context.Context, o *dao.Order) error {
	p, err := e.repo.GetOrderPayment(ctx, o.ID)
	if apperrors.Is(err, apperrors.NotFound) {
		return e.repo.WithTx(ctx, func(ctx context.Context) error {
			locked, err := e.repo.LockOrder(ctx, o.ID)
			if err != nil || locked.Status != dao.OrderPendingPayment {
				return err
			}
			return e.fail(ctx, locked, "expired unpaid")
		})
	}
	if err != nil {
		return err
	}
	if p.Status.IsOpen() {
		_, err = e.payments.Expire(ctx, p)
	}
	return err
}

// Order event topics, delivered to the owning product's backend.
const (
	TopicOrderPaid   = "order.paid"
	TopicOrderFailed = "order.failed"
)

type orderEvent struct {
	OrderID       string  `json:"order_id"`
	ExternalRef   string  `json:"external_ref"`
	ProductID     string  `json:"product_id"`
	CustomerID    *string `json:"customer_id,omitempty"`
	Status        string  `json:"status"`
	Total         int64   `json:"total"`
	Currency      string  `json:"currency"`
	FailureReason *string `json:"failure_reason,omitempty"`
}

func (e *Engine) emit(ctx context.Context, o *dao.Order, topic string) error {
	payload, err := json.Marshal(orderEvent{
		OrderID: o.PublicID, ExternalRef: o.ExternalRef, ProductID: o.ProductPublicID,
		CustomerID: o.CustomerPublicID, Status: string(o.Status), Total: o.Total, Currency: o.Currency,
		FailureReason: o.FailureReason,
	})
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to encode order event")
	}
	return e.repo.SaveOutboxEvent(ctx, &dao.OutboxEvent{
		EventID: ids.New(ids.OutboxEvent), Topic: topic,
		AggregateType: "order", AggregateID: o.PublicID, Payload: payload,
	})
}

// result assembles an order with its tenders and card payment.
func (e *Engine) result(ctx context.Context, o *dao.Order) (*Result, error) {
	tenders, err := e.repo.ListOrderTenders(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	r := &Result{Order: o, Tenders: tenders}
	if o.GatewayAmount > 0 {
		p, err := e.repo.GetOrderPayment(ctx, o.ID)
		if err != nil {
			return nil, err
		}
		r.Payment = p
	}
	return r, nil
}
