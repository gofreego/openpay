package payment

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/internal/wallet"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"

	"github.com/gofreego/goutils/logger"
)

// Repository is the storage the payment engine needs.
type Repository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	WithSavepoint(ctx context.Context, fn func(ctx context.Context) error) error

	CreatePayment(ctx context.Context, p *dao.Payment) error
	GetPaymentByID(ctx context.Context, id int64) (*dao.Payment, error)
	LockPayment(ctx context.Context, id int64) (*dao.Payment, error)
	UpdatePayment(ctx context.Context, p *dao.Payment) error
	CreatePaymentAttempt(ctx context.Context, a *dao.PaymentAttempt) error
	UpdatePaymentAttempt(ctx context.Context, a *dao.PaymentAttempt) error
	GetAttemptByProviderRef(ctx context.Context, providerName, providerPaymentID string) (*dao.PaymentAttempt, error)
	ListPaymentAttempts(ctx context.Context, paymentID int64) ([]*dao.PaymentAttempt, error)
	RecordPaymentTransition(ctx context.Context, t *dao.PaymentTransition) error

	GetProductByID(ctx context.Context, id int64) (*dao.Product, error)
	GetWalletByPublicID(ctx context.Context, publicID string) (*dao.Wallet, error)
	GetLedgerAccountByCode(ctx context.Context, code string) (*dao.LedgerAccount, error)
	PostJournal(ctx context.Context, journal *dao.Journal) error
	SaveOutboxEvent(ctx context.Context, event *dao.OutboxEvent) error

	RecordProviderRequest(ctx context.Context, req *dao.ProviderRequest) error

	RefundRepository
	DisputeRepository
}

// Config tunes payment timing.
type Config struct {
	// Providers lists provider names in order of preference.
	Providers []string `yaml:"Providers"`
	// TTL is how long a customer has to pay before the payment expires.
	TTL time.Duration `yaml:"TTL"`
	// PollAfter is how long an open payment may sit unchanged before the
	// poller asks the provider directly: the safety net for lost webhooks.
	PollAfter time.Duration `yaml:"PollAfter"`
	Mock      MockConfig    `yaml:"Mock"`
}

type MockConfig struct {
	Enabled       bool   `yaml:"Enabled"`
	WebhookSecret string `yaml:"WebhookSecret"`
	// CheckoutURL is the base of the mock's hosted page; the payment id is appended.
	CheckoutURL string `yaml:"CheckoutURL"`
}

func (c *Config) WithDefaults() {
	if c.TTL <= 0 {
		c.TTL = 30 * time.Minute
	}
	if c.PollAfter <= 0 {
		c.PollAfter = 2 * time.Minute
	}
}

type Engine struct {
	orders    OrderHook
	repo      Repository
	providers *provider.Registry
	wallets   *wallet.Engine
	cfg       Config
	now       func() time.Time
}

// New builds the engine. Every provider it is given is wrapped so its calls
// land in the request log.
func New(repo Repository, registry *provider.Registry, wallets *wallet.Engine, cfg Config) *Engine {
	cfg.WithDefaults()
	return &Engine{repo: repo, providers: registry, wallets: wallets, cfg: cfg, now: time.Now}
}

func (e *Engine) provider(name string) (provider.Provider, error) {
	p, err := e.providers.Get(name)
	if err != nil {
		return nil, err
	}
	return withRequestLog(p, e.repo), nil
}

// TopupRequest starts collecting money for a wallet.
type TopupRequest struct {
	// ProductID is the product whose checkout collects it.
	ProductID   int64
	Customer    *dao.Customer
	Wallet      *dao.Wallet
	Amount      int64
	Currency    string
	Description string
	ReturnURL   string
}

// CreateTopup opens a payment and its first attempt, and asks the provider
// for a checkout. It runs in the caller's transaction.
//
// A provider that refuses or times out does not fail the call: the payment
// is recorded as failed, with why, and returned — the caller asked what
// happened and that is the answer. A timeout leaves the provider's side
// unknown, but no checkout reached the customer, so nothing can be paid.
func (e *Engine) CreateTopup(ctx context.Context, req TopupRequest) (*dao.Payment, *dao.PaymentAttempt, error) {
	if req.Wallet.CustomerID != req.Customer.ID {
		return nil, nil, apperrors.New(apperrors.InvalidArgument, "the wallet does not belong to this customer")
	}
	if err := e.wallets.CheckFund(ctx, req.Wallet, req.Amount); err != nil {
		return nil, nil, err
	}
	product, err := e.repo.GetProductByID(ctx, req.ProductID)
	if err != nil {
		return nil, nil, err
	}
	return e.open(ctx, &dao.Payment{
		PublicID: ids.New(ids.Payment), ProductID: req.ProductID, ProductPublicID: product.PublicID,
		CustomerID: &req.Customer.ID, CustomerPublicID: &req.Customer.PublicID,
		Purpose: dao.PurposeWalletTopup, WalletID: &req.Wallet.ID, WalletPublicID: &req.Wallet.PublicID,
		Amount: req.Amount, Currency: req.Currency,
		Description: req.Description, ReturnURL: req.ReturnURL,
	})
}

// OrderPaymentRequest collects an order's card share.
type OrderPaymentRequest struct {
	ProductID   int64
	Customer    *dao.Customer // nil for a guest
	OrderID     int64
	Amount      int64
	Currency    string
	Description string
	ReturnURL   string
	// ExpiresAt matches the order's, so the two lapse together.
	ExpiresAt time.Time
}

// CreateOrderPayment opens the gateway payment for an order. Its capture and
// its failure are handed to the OrderHook, in the payment's own transaction.
func (e *Engine) CreateOrderPayment(ctx context.Context, req OrderPaymentRequest) (*dao.Payment, *dao.PaymentAttempt, error) {
	product, err := e.repo.GetProductByID(ctx, req.ProductID)
	if err != nil {
		return nil, nil, err
	}
	payment := &dao.Payment{
		PublicID: ids.New(ids.Payment), ProductID: req.ProductID, ProductPublicID: product.PublicID,
		Purpose: dao.PurposeOrder, OrderID: &req.OrderID,
		Amount: req.Amount, Currency: req.Currency,
		Description: req.Description, ReturnURL: req.ReturnURL, ExpiresAt: req.ExpiresAt,
	}
	if req.Customer != nil {
		payment.CustomerID, payment.CustomerPublicID = &req.Customer.ID, &req.Customer.PublicID
	}
	return e.open(ctx, payment)
}

// open records a payment and its first attempt, and asks the provider for a
// checkout. It runs in the caller's transaction.
//
// A provider that refuses or times out does not fail the call: the payment
// is recorded as failed, with why, and returned — the caller asked what
// happened and that is the answer. A timeout leaves the provider's side
// unknown, but no checkout reached the customer, so nothing can be paid.
func (e *Engine) open(ctx context.Context, payment *dao.Payment) (*dao.Payment, *dao.PaymentAttempt, error) {
	chosen, reason, err := e.providers.Choose()
	if err != nil {
		return nil, nil, err
	}
	p, err := e.provider(chosen.Name())
	if err != nil {
		return nil, nil, err
	}

	name := chosen.Name()
	payment.Status, payment.Application, payment.Provider = dao.PaymentCreated, dao.ApplicationPending, &name
	if payment.ExpiresAt.IsZero() {
		payment.ExpiresAt = e.now().Add(e.cfg.TTL)
	}

	var attempt *dao.PaymentAttempt
	err = e.repo.WithTx(ctx, func(ctx context.Context) error {
		if err := e.repo.CreatePayment(ctx, payment); err != nil {
			return err
		}
		attempt = &dao.PaymentAttempt{
			PublicID: ids.New(ids.PaymentAttempt), PaymentID: payment.ID,
			Provider: name, RoutingReason: reason, Status: string(provider.StatusCreated),
		}
		if err := e.repo.CreatePaymentAttempt(ctx, attempt); err != nil {
			return err
		}

		result, err := p.CreatePayment(ctx, provider.CreateRequest{
			AttemptID: attempt.PublicID, Amount: payment.Amount, Currency: payment.Currency,
			Description: payment.Description, ReturnURL: payment.ReturnURL,
		})
		if err != nil {
			code, detail := "technical", err.Error()
			if apperrors.Is(err, apperrors.WalletOperationDenied) || apperrors.Is(err, apperrors.FailedPrecondition) {
				code = "declined"
			}
			attempt.Status = string(provider.StatusFailed)
			attempt.FailureCode, attempt.FailureReason = &code, &detail
			if err := e.repo.UpdatePaymentAttempt(ctx, attempt); err != nil {
				return err
			}
			payment.FailureCode, payment.FailureReason = &code, &detail
			return e.transition(ctx, payment, dao.PaymentFailed, "api", attempt.PublicID, detail)
		}

		attempt.ProviderPaymentID = &result.ProviderPaymentID
		attempt.CheckoutURL = &result.Checkout.URL
		attempt.Status = string(provider.StatusPending)
		if err := e.repo.UpdatePaymentAttempt(ctx, attempt); err != nil {
			return err
		}
		return e.transition(ctx, payment, dao.PaymentPending, "api", attempt.PublicID, "")
	})
	if err != nil {
		return nil, nil, err
	}
	return payment, attempt, nil
}

// OrderHook is how an order learns what happened to its payment. Both calls
// run inside the payment's transaction, so an order can never disagree with
// its payment — even across a crash.
type OrderHook interface {
	// CaptureOrder books a captured order payment as the order's settlement.
	// applied is false when the order can no longer take it (it already
	// failed, or its wallet share is gone); the money is then owed back.
	CaptureOrder(ctx context.Context, payment *dao.Payment, providerName string) (applied bool, err error)
	// PaymentEnded tells the order its payment failed, expired or was
	// cancelled, so it can release what it holds.
	PaymentEnded(ctx context.Context, payment *dao.Payment) error
}

// SetOrderHook connects the order engine. Order payments are refused until
// one is set, rather than captured with nowhere to go.
func (e *Engine) SetOrderHook(h OrderHook) { e.orders = h }

// Sync brings a payment in line with the provider's authoritative view of
// one of its attempts. It is the only way a payment advances after creation:
// webhooks, the poller, the expiry sweeper and an operator's manual sync all
// come through here, so they cannot disagree about what a status means.
//
// It locks the payment, so concurrent syncs serialise. A status the state
// machine does not allow from here — typically a stale, out-of-order one —
// changes nothing.
func (e *Engine) Sync(ctx context.Context, attempt *dao.PaymentAttempt, pp *provider.Payment, source, reference string) (*dao.Payment, error) {
	var payment *dao.Payment
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		if payment, err = e.repo.LockPayment(ctx, attempt.PaymentID); err != nil {
			return err
		}

		attempt.Status = string(pp.Status)
		if pp.FailureCode != "" {
			attempt.FailureCode, attempt.FailureReason = &pp.FailureCode, &pp.FailureReason
		}
		if err := e.repo.UpdatePaymentAttempt(ctx, attempt); err != nil {
			return err
		}

		target := fromProvider(pp.Status)
		if target == payment.Status || !canTransition(payment.Status, target) {
			return nil
		}

		detail := ""
		switch target {
		case dao.PaymentCaptured:
			if !payment.Status.IsOpen() {
				detail = "late capture after " + string(payment.Status)
			}
			if err := e.applyCapture(ctx, payment, attempt, pp); err != nil {
				return err
			}
		case dao.PaymentFailed, dao.PaymentCancelled:
			if pp.FailureCode != "" {
				payment.FailureCode, payment.FailureReason = &pp.FailureCode, &pp.FailureReason
			}
		}
		return e.transition(ctx, payment, target, source, reference, detail)
	})
	return payment, err
}

// applyCapture books captured money. It always lands somewhere in the
// ledger — the one outcome that must never happen is money the provider holds
// for us that our books do not show:
//
//   - the provider's figures match: credit the wallet (applied);
//   - the wallet refuses it (a balance or load limit reached since the
//     payment began): owe it back to the customer in refunds_payable
//     (unapplied), for a refund to settle;
//   - the provider reports a different amount or currency than we asked for:
//     park it in the provider's suspense account (suspense) for a person to
//     resolve. Crediting either figure would be a guess (plan.md D7).
//
// All three share one external id, so however many times a capture is seen,
// the money is booked once.
func (e *Engine) applyCapture(ctx context.Context, payment *dao.Payment, attempt *dao.PaymentAttempt, pp *provider.Payment) error {
	now := e.now()
	captured := pp.Amount
	payment.CapturedAmount, payment.CapturedAt = &captured, &now
	payment.FailureCode, payment.FailureReason = nil, nil
	externalID := "payment:" + payment.PublicID + ":capture"

	product, err := e.repo.GetProductByID(ctx, payment.ProductID)
	if err != nil {
		return err
	}

	if pp.Amount != payment.Amount || pp.Currency != payment.Currency {
		payment.Application = dao.ApplicationSuspense
		logger.Error(ctx, "payment %s: provider %s reports %d %s, we asked for %d %s — parked in suspense",
			payment.PublicID, attempt.Provider, pp.Amount, pp.Currency, payment.Amount, payment.Currency)
		return e.post(ctx, externalID, payment, attempt.Provider, ledger.PSPSuspense(attempt.Provider), pp.Amount, pp.Currency,
			"provider amount differs from the payment's")
	}

	if payment.Purpose == dao.PurposeOrder {
		return e.applyOrderCapture(ctx, payment, attempt, product, externalID, captured, pp.Currency)
	}

	walletRef := *payment.WalletPublicID
	w, err := e.repo.GetWalletByPublicID(ctx, walletRef)
	if err != nil {
		return err
	}
	credit := e.repo.WithSavepoint(ctx, func(ctx context.Context) error {
		_, err := e.wallets.Fund(ctx, wallet.FundRequest{
			Wallet: w, Amount: captured, Provider: attempt.Provider,
			ProductID: payment.ProductID, ExternalID: externalID,
		})
		return err
	})
	switch {
	case credit == nil:
		payment.Application = dao.ApplicationApplied
		return nil
	case apperrors.Is(credit, apperrors.WalletOperationDenied):
		payment.Application = dao.ApplicationUnapplied
		logger.Warn(ctx, "payment %s captured but wallet refused the credit (%v) — owed back to the customer",
			payment.PublicID, credit)
		return e.post(ctx, externalID, payment, attempt.Provider, ledger.ProductRefundsPayable(product.Code),
			captured, pp.Currency, "wallet refused the credit: "+credit.Error())
	default:
		return credit
	}
}

// applyOrderCapture hands a captured order payment to its order. The order
// posts one journal for the whole purchase — wallet shares and this card
// share together. If it cannot take the money (it already failed, or a
// wallet share is gone), it records that itself and the card money is owed
// back here instead.
func (e *Engine) applyOrderCapture(ctx context.Context, payment *dao.Payment, attempt *dao.PaymentAttempt,
	product *dao.Product, externalID string, captured int64, currency string) error {
	if e.orders == nil {
		return apperrors.New(apperrors.Internal, "order payment %s captured but no order engine is connected", payment.PublicID)
	}
	applied, err := e.orders.CaptureOrder(ctx, payment, attempt.Provider)
	if err != nil {
		return err
	}
	if applied {
		payment.Application = dao.ApplicationApplied
		return nil
	}
	payment.Application = dao.ApplicationUnapplied
	logger.Warn(ctx, "order payment %s captured but its order could not take it — owed back", payment.PublicID)
	return e.post(ctx, externalID, payment, attempt.Provider, ledger.ProductRefundsPayable(product.Code),
		captured, currency, "order could not take the payment")
}

// post books a capture that could not be applied: Dr the provider's
// receivable, Cr where it waits.
func (e *Engine) post(ctx context.Context, externalID string, payment *dao.Payment, providerName, creditCode string, amount int64, currency, memo string) error {
	receivable, err := e.repo.GetLedgerAccountByCode(ctx, ledger.PSPReceivable(providerName))
	if err != nil {
		return err
	}
	credit, err := e.repo.GetLedgerAccountByCode(ctx, creditCode)
	if err != nil {
		return err
	}
	source := "payment"
	return e.repo.PostJournal(ctx, &dao.Journal{
		PublicID: ids.New(ids.LedgerJournal), ExternalID: externalID, Kind: dao.JournalTopup,
		ProductID: &payment.ProductID, SourceKind: &source, SourceID: &payment.PublicID, Memo: memo,
		Postings: []*dao.Posting{
			{AccountID: receivable.ID, Direction: dao.Debit, Amount: amount, Currency: currency},
			{AccountID: credit.ID, Direction: dao.Credit, Amount: amount, Currency: currency},
		},
	})
}

// Payment event topics, published through the outbox to the owning product.
const (
	TopicPaymentSucceeded = "payment.succeeded"
	TopicPaymentFailed    = "payment.failed"
	TopicPaymentExpired   = "payment.expired"
	TopicPaymentCancelled = "payment.cancelled"
)

var terminalTopics = map[dao.PaymentStatus]string{
	dao.PaymentCaptured:  TopicPaymentSucceeded,
	dao.PaymentFailed:    TopicPaymentFailed,
	dao.PaymentExpired:   TopicPaymentExpired,
	dao.PaymentCancelled: TopicPaymentCancelled,
}

type paymentEvent struct {
	PaymentID      string  `json:"payment_id"`
	ProductID      string  `json:"product_id"`
	CustomerID     *string `json:"customer_id,omitempty"`
	WalletID       *string `json:"wallet_id,omitempty"`
	Purpose        string  `json:"purpose"`
	Status         string  `json:"status"`
	Amount         int64   `json:"amount"`
	CapturedAmount *int64  `json:"captured_amount,omitempty"`
	Currency       string  `json:"currency"`
	Application    string  `json:"application"`
	FailureCode    *string `json:"failure_code,omitempty"`
}

// transition moves a payment, records why, and emits its event — all in the
// caller's transaction, so the three happen together or not at all.
func (e *Engine) transition(ctx context.Context, payment *dao.Payment, to dao.PaymentStatus, source, reference, detail string) error {
	from := payment.Status
	if from != to && !canTransition(from, to) {
		return apperrors.New(apperrors.FailedPrecondition, "payment %s cannot move from %s to %s", payment.PublicID, from, to)
	}
	payment.Status = to
	if err := e.repo.UpdatePayment(ctx, payment); err != nil {
		return err
	}

	t := &dao.PaymentTransition{PaymentID: payment.ID, From: from, To: to, Source: source}
	if reference != "" {
		t.Reference = &reference
	}
	if detail != "" {
		t.Detail = &detail
	}
	if err := e.repo.RecordPaymentTransition(ctx, t); err != nil {
		return err
	}

	if payment.Purpose == dao.PurposeOrder && e.orders != nil &&
		(to == dao.PaymentFailed || to == dao.PaymentExpired || to == dao.PaymentCancelled) {
		if err := e.orders.PaymentEnded(ctx, payment); err != nil {
			return err
		}
	}

	topic, ok := terminalTopics[to]
	if !ok {
		return nil
	}
	payload, err := json.Marshal(paymentEvent{
		PaymentID: payment.PublicID, ProductID: payment.ProductPublicID,
		CustomerID: payment.CustomerPublicID, WalletID: payment.WalletPublicID,
		Purpose: string(payment.Purpose), Status: string(to), Amount: payment.Amount,
		CapturedAmount: payment.CapturedAmount, Currency: payment.Currency,
		Application: string(payment.Application), FailureCode: payment.FailureCode,
	})
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to encode payment event")
	}
	return e.repo.SaveOutboxEvent(ctx, &dao.OutboxEvent{
		EventID: ids.New(ids.OutboxEvent), Topic: topic,
		AggregateType: "payment", AggregateID: payment.PublicID, Payload: payload,
	})
}
