package service

import (
	"context"
	"time"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/internal/order"
	"github.com/gofreego/openpay/internal/payment"
	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/internal/recon"
	"github.com/gofreego/openpay/internal/wallet"
	"github.com/gofreego/openpay/internal/withdrawal"
	"github.com/gofreego/openpay/pkg/fieldcrypt"

	"github.com/gofreego/goutils/logger"
)

// mustCipher builds the field cipher or stops startup: without a key the only
// alternative would be storing bank accounts in plaintext.
func mustCipher(ctx context.Context, cfg fieldcrypt.Config) *fieldcrypt.Cipher {
	c, err := fieldcrypt.New(cfg)
	if err != nil {
		logger.Panic(ctx, "field encryption is not configured (Service.Encryption): %v", err)
	}
	return c
}

type Config struct {
	// Wallet holds the per-customer caps on real money (see wallet.Limits).
	Wallet wallet.Limits `yaml:"Wallet"`
	// Payments configures providers and payment timing.
	Payments payment.Config `yaml:"Payments"`
	// Orders configures how long an order's card share may take.
	Orders order.Config `yaml:"Orders"`
	// Recon configures settlement matching and when it alerts.
	Recon       recon.Config      `yaml:"Recon"`
	ReconAlerts recon.AlertConfig `yaml:"ReconAlerts"`
	// Withdrawals configures payouts: the bank they leave from, and how long
	// a new destination cools before it can receive money.
	Withdrawals withdrawal.Config `yaml:"Withdrawals"`
	// Encryption keys for sensitive fields (bank account numbers). Real keys
	// belong in a secret store; the service refuses to start without one.
	Encryption fieldcrypt.Config `yaml:"Encryption"`
}

type Repository interface {
	Ping(ctx context.Context) error

	// WithTx runs fn inside a single database transaction, committing when fn
	// returns nil and rolling back on error or panic. The transaction travels on
	// the context, so every repository call made with that ctx joins it.
	//
	// Nested calls join the outer transaction rather than opening a new one, so
	// a service method that calls another still forms one unit of work.
	//
	// Anything that moves money runs inside this. A ledger journal and its
	// postings and balance updates commit together or not at all.
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error

	IdempotencyRepository
	OutboxRepository
	ProductRepository
	CredentialRepository
	AuditRepository
	CustomerRepository
	WalletTypeRepository
	LedgerRepository
	WalletRepository
	PaymentRepository
	ReportRepository
}

// ReportRepository reads the figures reports are made of. Every period is
// half-open, [from, to).
type ReportRepository interface {
	ProductPnL(ctx context.Context, scope *filter.ProductScope, from, to time.Time) ([]*dao.PnLLine, error)
	ProviderStats(ctx context.Context, scope *filter.ProductScope, from, to time.Time) ([]*dao.ProviderStats, error)
	ListStatementRange(ctx context.Context, accountID int64, from, to time.Time, afterPostingID int64, limit int) ([]*dao.StatementEntry, error)
	OpsSnapshot(ctx context.Context, openBefore, holdsBefore time.Time) (*dao.OpsSnapshot, error)
}

type PaymentRepository interface {
	WithSavepoint(ctx context.Context, fn func(ctx context.Context) error) error

	CreatePayment(ctx context.Context, p *dao.Payment) error
	GetPaymentByID(ctx context.Context, id int64) (*dao.Payment, error)
	GetPaymentByPublicID(ctx context.Context, publicID string) (*dao.Payment, error)
	LockPayment(ctx context.Context, id int64) (*dao.Payment, error)
	UpdatePayment(ctx context.Context, p *dao.Payment) error
	ListPayments(ctx context.Context, f *filter.Payment) ([]*dao.Payment, int64, error)
	ListStalePayments(ctx context.Context, before time.Time, limit int) ([]*dao.Payment, error)
	ListExpiredPayments(ctx context.Context, now time.Time, limit int) ([]*dao.Payment, error)

	CreatePaymentAttempt(ctx context.Context, a *dao.PaymentAttempt) error
	UpdatePaymentAttempt(ctx context.Context, a *dao.PaymentAttempt) error
	GetAttemptByProviderRef(ctx context.Context, providerName, providerPaymentID string) (*dao.PaymentAttempt, error)
	ListPaymentAttempts(ctx context.Context, paymentID int64) ([]*dao.PaymentAttempt, error)
	RecordPaymentTransition(ctx context.Context, t *dao.PaymentTransition) error
	ListPaymentTransitions(ctx context.Context, paymentID int64) ([]*dao.PaymentTransition, error)

	SaveProviderEvent(ctx context.Context, e *dao.ProviderEvent) (inserted bool, err error)
	ClaimProviderEvent(ctx context.Context, now time.Time) (*dao.ProviderEvent, error)
	MarkProviderEventProcessed(ctx context.Context, id int64) error
	MarkProviderEventFailed(ctx context.Context, id int64, cause string, retryAt time.Time) error
	RecordProviderRequest(ctx context.Context, req *dao.ProviderRequest) error
	ListProviderControls(ctx context.Context) (map[string]provider.Control, error)
	SetProviderControl(ctx context.Context, name string, c provider.Control, by string) error

	PostJournal(ctx context.Context, journal *dao.Journal) error

	CreateRefund(ctx context.Context, refund *dao.Refund) error
	UpdateRefund(ctx context.Context, refund *dao.Refund) error
	LockRefund(ctx context.Context, id int64) (*dao.Refund, error)
	GetRefundByPublicID(ctx context.Context, publicID string) (*dao.Refund, error)
	GetRefundByProviderRef(ctx context.Context, providerName, providerRefundID string) (*dao.Refund, error)
	ListPaymentRefunds(ctx context.Context, paymentID int64) ([]*dao.Refund, error)
	ListOpenRefunds(ctx context.Context, before time.Time, limit int) ([]*dao.Refund, error)
	SumProcessedRefunds(ctx context.Context, paymentID int64) (int64, error)
	GetJournalByExternalID(ctx context.Context, externalID string) (*dao.Journal, error)

	CreateItem(ctx context.Context, item *dao.Item) error
	GetItemByPublicID(ctx context.Context, publicID string) (*dao.Item, error)
	ListItems(ctx context.Context, productID int64) ([]*dao.Item, error)
	CreateOrder(ctx context.Context, o *dao.Order) error
	UpdateOrder(ctx context.Context, o *dao.Order) error
	LockOrder(ctx context.Context, id int64) (*dao.Order, error)
	GetOrderByPublicID(ctx context.Context, publicID string) (*dao.Order, error)
	GetOrderByExternalRef(ctx context.Context, productID int64, externalRef string) (*dao.Order, error)
	ListExpiredOrders(ctx context.Context, now time.Time, limit int) ([]*dao.Order, error)
	CreateOrderLine(ctx context.Context, l *dao.OrderLine) error
	ListOrderLines(ctx context.Context, orderID int64) ([]*dao.OrderLine, error)
	CreateOrderTender(ctx context.Context, t *dao.OrderTender) error
	UpdateOrderTender(ctx context.Context, t *dao.OrderTender) error
	ListOrderTenders(ctx context.Context, orderID int64) ([]*dao.OrderTender, error)
	GetOrderPayment(ctx context.Context, orderID int64) (*dao.Payment, error)
	GetHoldByExternalID(ctx context.Context, externalID string) (*dao.Hold, error)
	CaptureHolds(ctx context.Context, holdExternalIDs []string, journal *dao.Journal) ([]*dao.Hold, bool, error)
	CreateOrderRefund(ctx context.Context, x *dao.OrderRefund) error
	CreateOrderRefundPart(ctx context.Context, p *dao.OrderRefundPart) error
	ListOrderRefunds(ctx context.Context, orderID int64) ([]*dao.OrderRefund, error)
	ListOrderRefundParts(ctx context.Context, orderRefundID int64) ([]*dao.OrderRefundPart, error)

	CreateSettlement(ctx context.Context, s *dao.Settlement) (bool, error)
	SetSettlementStatus(ctx context.Context, id int64, status dao.SettlementStatus) error
	LatestSettlementAt(ctx context.Context, providerName string) (time.Time, error)
	GetSettlementByPublicID(ctx context.Context, publicID string) (*dao.Settlement, error)
	ListSettlements(ctx context.Context, providerName string, limit int) ([]*dao.Settlement, error)
	CreateSettlementItem(ctx context.Context, it *dao.SettlementItem) error
	ListSettlementItems(ctx context.Context, settlementID int64) ([]*dao.SettlementItem, error)
	IsSettled(ctx context.Context, kind, providerRef string) (bool, error)
	SettlementByProduct(ctx context.Context, settlementID int64) ([]dao.SettlementProductShare, error)
	FeeVariance(ctx context.Context, from, to time.Time) ([]dao.FeeVarianceLine, error)
	CreateBreak(ctx context.Context, b *dao.ReconBreak) (bool, error)
	UpdateBreak(ctx context.Context, b *dao.ReconBreak) error
	LockBreak(ctx context.Context, publicID string) (*dao.ReconBreak, error)
	OpenMissingAtProviderBreak(ctx context.Context, paymentID int64) (*dao.ReconBreak, error)
	ListBreaks(ctx context.Context, scope *filter.ProductScope, status dao.BreakStatus, limit int) ([]*dao.ReconBreak, error)
	BreakSummary(ctx context.Context, agedBefore time.Time) (open, aged int64, err error)
	ListUnsettledPayments(ctx context.Context, providerName string, capturedBefore time.Time, limit int) ([]*dao.Payment, error)

	CreateBeneficiary(ctx context.Context, b *dao.Beneficiary) error
	GetBeneficiaryByID(ctx context.Context, id int64) (*dao.Beneficiary, error)
	GetBeneficiaryByPublicID(ctx context.Context, publicID string) (*dao.Beneficiary, error)
	ListBeneficiaries(ctx context.Context, customerID int64) ([]*dao.Beneficiary, error)
	CreateWithdrawal(ctx context.Context, x *dao.Withdrawal) error
	UpdateWithdrawal(ctx context.Context, x *dao.Withdrawal) error
	LockWithdrawal(ctx context.Context, id int64) (*dao.Withdrawal, error)
	ListBeneficiariesToReseal(ctx context.Context, keyID string) ([]*dao.Beneficiary, error)
	ResealBeneficiaryAccount(ctx context.Context, id int64, previous, sealed, last4, fingerprint string) (bool, error)
	GetWithdrawalByPublicID(ctx context.Context, publicID string) (*dao.Withdrawal, error)
	GetWithdrawalByProviderRef(ctx context.Context, providerName, providerPayoutID string) (*dao.Withdrawal, error)
	ListWithdrawals(ctx context.Context, scope *filter.ProductScope, status dao.WithdrawalStatus, customerID *int64, limit int) ([]*dao.Withdrawal, error)
	ListOpenWithdrawals(ctx context.Context, before time.Time, limit int) ([]*dao.Withdrawal, error)
	WithdrawnSince(ctx context.Context, customerID int64, walletID *int64, since time.Time) (amount int64, count int64, err error)

	CreateDispute(ctx context.Context, d *dao.Dispute) error
	UpdateDispute(ctx context.Context, d *dao.Dispute) error
	LockDispute(ctx context.Context, id int64) (*dao.Dispute, error)
	GetDisputeByPublicID(ctx context.Context, publicID string) (*dao.Dispute, error)
	GetDisputeByProviderRef(ctx context.Context, providerName, providerDisputeID string) (*dao.Dispute, error)
	ListDisputes(ctx context.Context, scope *filter.ProductScope, status dao.DisputeStatus, limit int) ([]*dao.Dispute, error)
	ListOpenDisputes(ctx context.Context, before time.Time, limit int) ([]*dao.Dispute, error)
}

type WalletRepository interface {
	GetProductByID(ctx context.Context, id int64) (*dao.Product, error)
	GetWalletTypeByID(ctx context.Context, id int64) (*dao.WalletType, error)
	GetLedgerAccountByCode(ctx context.Context, code string) (*dao.LedgerAccount, error)
	GetBalance(ctx context.Context, accountID int64) (*dao.Balance, error)

	GetOrCreateWallet(ctx context.Context, wallet *dao.Wallet) (created bool, err error)
	GetWallet(ctx context.Context, customerID, walletTypeID int64) (*dao.Wallet, error)
	GetWalletByPublicID(ctx context.Context, publicID string) (*dao.Wallet, error)
	ListCustomerWallets(ctx context.Context, customerID int64, scope *filter.ProductScope, includePlatform bool) ([]*dao.Wallet, error)
	SumWalletLoadsSince(ctx context.Context, accountID int64, since time.Time) (int64, error)
	LatestPostingID(ctx context.Context, accountID, excludingJournalID int64) (int64, error)
	ListRollingExpiryCandidates(ctx context.Context, now time.Time, limit int) ([]dao.ExpiryCandidate, error)

	LockCustomer(ctx context.Context, customerID int64) error
	CustomerFundedBalance(ctx context.Context, customerID int64) (int64, error)
	CustomerFundedLoadsSince(ctx context.Context, customerID int64, since time.Time) (int64, error)

	PostJournalChecked(ctx context.Context, journal *dao.Journal, check func(ctx context.Context) error) (posted bool, err error)
	PlaceHold(ctx context.Context, hold *dao.Hold) error
	CaptureHold(ctx context.Context, holdExternalID string, journal *dao.Journal) (hold *dao.Hold, captured bool, err error)
	ReleaseHold(ctx context.Context, holdExternalID string) (*dao.Hold, error)
	ExpireHold(ctx context.Context, holdExternalID string) (*dao.Hold, error)
	ListExpiredHolds(ctx context.Context, now time.Time, limit int) ([]string, error)
	FloatHeld(ctx context.Context, scope *filter.ProductScope) ([]*dao.FloatLine, error)
}

type LedgerRepository interface {
	// GetOrCreateLedgerAccount opens an account unless one with the same code
	// exists, in which case it loads that one into account.
	GetOrCreateLedgerAccount(ctx context.Context, account *dao.LedgerAccount) (created bool, err error)

	// Read path. ref is a public id or, if it contains ':', a code or
	// external id.
	GetAccountView(ctx context.Context, ref string) (*dao.AccountView, error)
	ListAccountViews(ctx context.Context, f *filter.LedgerAccount) ([]*dao.AccountView, int64, error)
	ListStatement(ctx context.Context, accountID int64, limit int, beforePostingID int64) ([]*dao.StatementEntry, error)
	GetJournalView(ctx context.Context, ref string) (*dao.Journal, error)
	TrialBalance(ctx context.Context, productID *int64, asOf time.Time, includeEmpty bool) ([]*dao.TrialBalanceLine, error)

	// Invariant checks (see ledger.RunCheck).
	CheckLedgerInvariants(ctx context.Context, limit int) ([]dao.LedgerCheckFinding, bool, error)
	RecordLedgerCheckRun(ctx context.Context, run *dao.LedgerCheckRun) error
	ListLedgerCheckRuns(ctx context.Context, limit int) ([]*dao.LedgerCheckRun, error)
}

type CustomerRepository interface {
	// UpsertCustomer creates a customer or returns the existing one for the
	// same external_ref, resolving through a merge if the record found is a
	// tombstone. created distinguishes a first registration from a repeat.
	UpsertCustomer(ctx context.Context, customer *dao.Customer) (created bool, err error)
	GetCustomerByExternalRef(ctx context.Context, externalRef string) (*dao.Customer, error)
	GetCustomerByPublicID(ctx context.Context, publicID string) (*dao.Customer, error)
}

type WalletTypeRepository interface {
	CreateWalletType(ctx context.Context, walletType *dao.WalletType) error
	GetWalletTypeByPublicID(ctx context.Context, publicID string) (*dao.WalletType, error)
	// ListWalletTypes returns a product's own types plus the platform-scoped
	// ones, since both are spendable within that product.
	ListWalletTypes(ctx context.Context, productID int64) ([]*dao.WalletType, error)
	UpdateWalletType(ctx context.Context, walletType *dao.WalletType) error
}

type ProductRepository interface {
	CreateProduct(ctx context.Context, product *dao.Product) error
	GetProductByPublicID(ctx context.Context, publicID string) (*dao.Product, error)
	GetProductByCode(ctx context.Context, code string) (*dao.Product, error)
	ListProducts(ctx context.Context, f *filter.Product) ([]*dao.Product, int64, error)
	UpdateProduct(ctx context.Context, product *dao.Product) error
}

type CredentialRepository interface {
	CreateCredential(ctx context.Context, credential *dao.ServiceCredential) error

	// GetCredentialByKeyID loads a credential and the product it belongs to.
	// Both are needed on every authenticated call: a credential for an inactive
	// product must not authenticate.
	GetCredentialByKeyID(ctx context.Context, keyID string) (*dao.ServiceCredential, *dao.Product, error)

	ListCredentials(ctx context.Context, productID int64) ([]*dao.ServiceCredential, error)
	RevokeCredential(ctx context.Context, publicID string) error

	// TouchCredentialUsed records that a credential was used. It is best-effort
	// and deliberately outside the caller's transaction: it answers "is this
	// still in use?" before revoking, and must never fail a payment.
	TouchCredentialUsed(ctx context.Context, id int64) error
}

type AuditRepository interface {
	// RecordAudit appends an audit entry. Call it inside the transaction that
	// makes the change, so an action and its record commit together.
	RecordAudit(ctx context.Context, entry *dao.AuditEntry) error
}

// IdempotencyRepository backs retry-safe mutations (plan.md D4).
//
// The intended sequence is:
//
//	ClaimIdempotencyKey        — on its own, committed immediately so concurrent
//	                             duplicates can see the claim
//	WithTx( work; CompleteIdempotencyKey )
//	                           — response recorded in the same commit as the work,
//	                             so a stored response always implies the work happened
//	ReleaseIdempotencyKey      — on failure, so an immediate retry can re-run
type IdempotencyRepository interface {
	// ClaimIdempotencyKey attempts to reserve (scope, key). It returns the
	// existing record when someone else got there first, and claimed=false.
	// The insert-or-nothing must be atomic: two concurrent identical requests
	// must not both come back claimed.
	ClaimIdempotencyKey(ctx context.Context, key *dao.IdempotencyKey) (existing *dao.IdempotencyKey, claimed bool, err error)

	// CompleteIdempotencyKey records the response. Call it inside the same
	// transaction as the work, so the two commit together.
	CompleteIdempotencyKey(ctx context.Context, scope, key string, response []byte) error

	// ReleaseIdempotencyKey drops an in-progress claim so a retry can re-run.
	// Only failed work releases: successful work has already recorded a response.
	ReleaseIdempotencyKey(ctx context.Context, scope, key string) error

	// DeleteExpiredIdempotencyKeys prunes the table and frees keys abandoned by
	// a crashed process. Returns how many rows went.
	DeleteExpiredIdempotencyKeys(ctx context.Context, limit int) (int64, error)
}

// OutboxRepository backs reliable event publishing (plan.md D5).
type OutboxRepository interface {
	// SaveOutboxEvent records an event. Call it inside the transaction that
	// makes the change it describes, never outside.
	SaveOutboxEvent(ctx context.Context, event *dao.OutboxEvent) error

	// ClaimUnpublishedOutboxEvents locks a batch of unpublished events for this
	// worker, skipping rows another worker holds, and returns them oldest first.
	// Must be called inside a transaction: the locks live until it ends.
	ClaimUnpublishedOutboxEvents(ctx context.Context, limit int) ([]*dao.OutboxEvent, error)

	// MarkOutboxEventPublished stamps an event as delivered.
	MarkOutboxEventPublished(ctx context.Context, id int64) error

	// MarkOutboxEventFailed records a delivery attempt that did not succeed, so
	// a stuck event is visible rather than silently retried forever.
	MarkOutboxEventFailed(ctx context.Context, id int64, cause string) error
}

type Service struct {
	repo        Repository
	wallets     *wallet.Engine
	payments    *payment.Engine
	orders      *order.Engine
	recon       *recon.Engine
	alerts      recon.AlertConfig
	withdrawals *withdrawal.Engine
	registry    *provider.Registry
	openpay_v1.UnimplementedOpenPayServer
}

func NewService(ctx context.Context, cfg *Config, repo Repository) *Service {
	wallets := wallet.New(repo, cfg.Wallet)
	registry, _ := payment.Providers(cfg.Payments)
	payments := payment.New(repo, registry, wallets, cfg.Payments)
	return &Service{
		repo:     repo,
		wallets:  wallets,
		payments: payments,
		// Connects itself to payments: order card shares are settled or
		// failed from inside the payment's own transaction.
		orders: order.New(repo, wallets, payments, cfg.Orders),
		recon:  recon.New(repo, registry, payments, cfg.Recon),
		alerts: cfg.ReconAlerts,
		// Connects itself to payments, which hands it payout webhooks.
		withdrawals: withdrawal.New(repo, registry, wallets, payments, mustCipher(ctx, cfg.Encryption), cfg.Withdrawals),
		registry:    registry,
	}
}
