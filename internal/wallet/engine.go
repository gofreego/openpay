package wallet

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

// Repository is the storage the wallet engine needs.
type Repository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error

	GetProductByID(ctx context.Context, id int64) (*dao.Product, error)
	GetWalletTypeByID(ctx context.Context, id int64) (*dao.WalletType, error)

	GetOrCreateLedgerAccount(ctx context.Context, account *dao.LedgerAccount) (created bool, err error)
	GetLedgerAccountByCode(ctx context.Context, code string) (*dao.LedgerAccount, error)
	GetBalance(ctx context.Context, accountID int64) (*dao.Balance, error)

	GetOrCreateWallet(ctx context.Context, wallet *dao.Wallet) (created bool, err error)
	GetWalletByPublicID(ctx context.Context, publicID string) (*dao.Wallet, error)
	SumWalletLoadsSince(ctx context.Context, accountID int64, since time.Time) (int64, error)
	LatestPostingID(ctx context.Context, accountID, excludingJournalID int64) (int64, error)

	LockCustomer(ctx context.Context, customerID int64) error
	CustomerFundedBalance(ctx context.Context, customerID int64) (int64, error)
	CustomerFundedLoadsSince(ctx context.Context, customerID int64, since time.Time) (int64, error)

	PostJournalChecked(ctx context.Context, journal *dao.Journal, check func(ctx context.Context) error) (posted bool, err error)
	SaveOutboxEvent(ctx context.Context, event *dao.OutboxEvent) error
	PlaceHold(ctx context.Context, hold *dao.Hold) error
	CaptureHold(ctx context.Context, holdExternalID string, journal *dao.Journal) (hold *dao.Hold, captured bool, err error)
	ReleaseHold(ctx context.Context, holdExternalID string) (*dao.Hold, error)
}

// ist is the day boundary for daily limits. v1 is India-only (plan.md D11),
// and a customer's "today" is the Indian day, not UTC's — which would roll
// over at 05:30. India has no daylight saving, so a fixed zone is exact and
// needs no tzdata in the container.
var ist = time.FixedZone("IST", 5*60*60+30*60)

// Reason codes are fixed lists (plan.md U-D7), so what was given away and why
// can be reported on rather than read out of free text.
var (
	GrantReasons  = []string{"promotion", "referral", "cashback", "loyalty", "compensation"}
	AdjustReasons = []string{"goodwill", "error_correction", "fraud_recovery", "migration"}
)

// Limits are per-person caps spanning every fundable wallet a customer holds,
// in every product. They are distinct from a wallet type's limits: a customer
// is platform-wide, and if a PPI licence is ever in scope (plan.md Q1) its
// limits apply per person, not per wallet. Zero means no cap.
type Limits struct {
	// MaxCustomerBalance caps the real money held for one person.
	MaxCustomerBalance int64 `yaml:"MaxCustomerBalance"`
	// MaxCustomerDailyLoad caps what one person may load in a day.
	MaxCustomerDailyLoad int64 `yaml:"MaxCustomerDailyLoad"`
}

type Engine struct {
	repo   Repository
	limits Limits
	now    func() time.Time
}

func New(repo Repository, limits Limits) *Engine {
	return &Engine{repo: repo, limits: limits, now: time.Now}
}

// Open returns the customer's wallet of a type, creating it and its ledger
// account on first reference. Safe to call repeatedly and concurrently;
// created reports whether this call made it.
func (e *Engine) Open(ctx context.Context, customer *dao.Customer, walletType *dao.WalletType) (wallet *dao.Wallet, created bool, err error) {
	if customer.Status != dao.CustomerActive {
		return nil, false, denied("customer %s is %s", customer.PublicID, customer.Status)
	}

	err = e.repo.WithTx(ctx, func(ctx context.Context) error {
		productSegment := ledger.PlatformScope
		if walletType.ProductID != nil {
			product, err := e.repo.GetProductByID(ctx, *walletType.ProductID)
			if err != nil {
				return err
			}
			productSegment = product.Code
		}

		account := &dao.LedgerAccount{
			PublicID: ids.New(ids.LedgerAccount),
			Code:     ledger.WalletAccount(customer.PublicID, productSegment, walletType.Code),
			// A wallet is a liability: it is money we owe the customer.
			Type:          dao.AccountLiability,
			ProductID:     walletType.ProductID,
			Currency:      walletType.Currency,
			OwnerKind:     dao.OwnerCustomer,
			OwnerID:       &customer.PublicID,
			AllowNegative: walletType.AllowNegative,
			Status:        dao.AccountActive,
		}
		if _, err := e.repo.GetOrCreateLedgerAccount(ctx, account); err != nil {
			return err
		}

		wallet = &dao.Wallet{
			PublicID:         ids.New(ids.Wallet),
			CustomerID:       customer.ID,
			CustomerPublicID: customer.PublicID,
			WalletTypeID:     walletType.ID,
			ProductID:        walletType.ProductID,
			LedgerAccountID:  account.ID,
			Status:           dao.WalletActive,
		}
		created, err = e.repo.GetOrCreateWallet(ctx, wallet)
		if err != nil {
			return err
		}
		// An archived type keeps serving the wallets it already has; it only
		// stops new ones.
		if created && walletType.Status != dao.WalletTypeActive {
			return denied("wallet type %s is %s and cannot open new wallets", walletType.Code, walletType.Status)
		}
		return nil
	})
	return wallet, created, err
}

// GrantRequest credits promotional value.
type GrantRequest struct {
	Wallet *dao.Wallet
	Amount int64
	// FundingProductID is whose promotions budget pays. For a product-scoped
	// wallet it must be that product; a platform wallet can be granted to by
	// any product, which then carries the cost.
	FundingProductID int64
	ReasonCode       string
	Memo             string
	// Reference is the caller's id for this grant, making it idempotent.
	Reference string
}

// Grant credits a grantable wallet, funded from the product's promotions
// expense: Dr expense:<product>:promotions, Cr wallet.
func (e *Engine) Grant(ctx context.Context, req GrantRequest) (*dao.Journal, error) {
	if !slices.Contains(GrantReasons, req.ReasonCode) {
		return nil, apperrors.New(apperrors.InvalidArgument,
			"reason code %q is not one of %v", req.ReasonCode, GrantReasons)
	}
	if req.Wallet.ProductID != nil && *req.Wallet.ProductID != req.FundingProductID {
		return nil, denied("a product can only fund grants to its own wallets")
	}

	var journal *dao.Journal
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		walletType, err := e.repo.GetWalletTypeByID(ctx, req.Wallet.WalletTypeID)
		if err != nil {
			return err
		}
		if err := assertAllowed(walletType, req.Wallet, OpGrant, req.Amount); err != nil {
			return err
		}
		product, err := e.repo.GetProductByID(ctx, req.FundingProductID)
		if err != nil {
			return err
		}
		promotions, err := e.repo.GetLedgerAccountByCode(ctx, ledger.ProductPromotions(product.Code))
		if err != nil {
			return err
		}

		journal = newJournal("grant:"+req.Wallet.PublicID+":"+req.Reference, dao.JournalGrant, &product.ID, req.Wallet)
		journal.ReasonCode = &req.ReasonCode
		journal.Memo = req.Memo
		journal.Postings = []*dao.Posting{
			leg(promotions.ID, dao.Debit, req.Amount, walletType.Currency),
			leg(req.Wallet.LedgerAccountID, dao.Credit, req.Amount, walletType.Currency),
		}
		return e.post(ctx, journal, req.Wallet, dao.Credit, req.Amount,
			limitCheck{wallet: req.Wallet, walletType: walletType, op: OpGrant})
	})
	return journal, err
}

// FundRequest loads money the customer paid for.
type FundRequest struct {
	Wallet   *dao.Wallet
	Amount   int64
	Provider string
	// ProductID is the product whose checkout took the payment. For a
	// platform wallet it is where the top-up (and its fee) is attributed.
	ProductID  int64
	ExternalID string
}

// Fund credits a fundable wallet from a captured payment:
// Dr psp:<provider>:receivable, Cr wallet. The amount must be the provider's
// figure, never the client's (plan.md D7) — that is the caller's duty.
func (e *Engine) Fund(ctx context.Context, req FundRequest) (*dao.Journal, error) {
	var journal *dao.Journal
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		walletType, err := e.repo.GetWalletTypeByID(ctx, req.Wallet.WalletTypeID)
		if err != nil {
			return err
		}
		if err := assertAllowed(walletType, req.Wallet, OpFund, req.Amount); err != nil {
			return err
		}
		receivable, err := e.repo.GetLedgerAccountByCode(ctx, ledger.PSPReceivable(req.Provider))
		if err != nil {
			return err
		}

		journal = newJournal(req.ExternalID, dao.JournalTopup, &req.ProductID, req.Wallet)
		journal.Postings = []*dao.Posting{
			leg(receivable.ID, dao.Debit, req.Amount, walletType.Currency),
			leg(req.Wallet.LedgerAccountID, dao.Credit, req.Amount, walletType.Currency),
		}
		return e.post(ctx, journal, req.Wallet, dao.Credit, req.Amount,
			limitCheck{wallet: req.Wallet, walletType: walletType, op: OpFund})
	})
	return journal, err
}

// SpendRequest takes value out of a wallet to pay for something.
type SpendRequest struct {
	Wallet *dao.Wallet
	Amount int64
	// CounterAccountCode is where the value goes, e.g. the product's sales
	// income. Orders (plan.md phase 7) own that decision, with its tax split.
	CounterAccountCode string
	ProductID          int64
	Kind               dao.JournalKind
	ExternalID         string
}

// Spend debits a wallet: Dr wallet, Cr the counter account. Overdraft is the
// ledger's to refuse, using the wallet account's allow_negative.
func (e *Engine) Spend(ctx context.Context, req SpendRequest) (*dao.Journal, error) {
	var journal *dao.Journal
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		walletType, err := e.repo.GetWalletTypeByID(ctx, req.Wallet.WalletTypeID)
		if err != nil {
			return err
		}
		if err := assertAllowed(walletType, req.Wallet, OpSpend, req.Amount); err != nil {
			return err
		}
		counter, err := e.repo.GetLedgerAccountByCode(ctx, req.CounterAccountCode)
		if err != nil {
			return err
		}

		journal = newJournal(req.ExternalID, req.Kind, &req.ProductID, req.Wallet)
		journal.Postings = []*dao.Posting{
			leg(req.Wallet.LedgerAccountID, dao.Debit, req.Amount, walletType.Currency),
			leg(counter.ID, dao.Credit, req.Amount, walletType.Currency),
		}
		return e.post(ctx, journal, req.Wallet, dao.Debit, req.Amount, limitCheck{})
	})
	return journal, err
}

// TransferRequest moves value between two customers' wallets.
type TransferRequest struct {
	From, To  *dao.Wallet
	Amount    int64
	Reference string
}

// Transfer moves value between two wallets of the same type: Dr from, Cr to.
//
// Same type only. A MAIN→BONUS transfer would turn purchased money into
// granted money (or the reverse), which is exactly the mixing D10 keeps the
// types apart to prevent.
func (e *Engine) Transfer(ctx context.Context, req TransferRequest) (*dao.Journal, error) {
	if req.From.WalletTypeID != req.To.WalletTypeID {
		return nil, denied("transfers are only between wallets of the same type")
	}
	if req.From.ID == req.To.ID {
		return nil, apperrors.New(apperrors.InvalidArgument, "cannot transfer a wallet to itself")
	}

	var journal *dao.Journal
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		walletType, err := e.repo.GetWalletTypeByID(ctx, req.From.WalletTypeID)
		if err != nil {
			return err
		}
		if err := assertAllowed(walletType, req.From, OpTransferOut, req.Amount); err != nil {
			return err
		}
		if err := assertAllowed(walletType, req.To, OpTransferIn, req.Amount); err != nil {
			return err
		}

		journal = newJournal("transfer:"+req.From.PublicID+":"+req.Reference, dao.JournalTransfer, req.From.ProductID, req.From)
		journal.Postings = []*dao.Posting{
			leg(req.From.LedgerAccountID, dao.Debit, req.Amount, walletType.Currency),
			leg(req.To.LedgerAccountID, dao.Credit, req.Amount, walletType.Currency),
		}
		return e.post(ctx, journal, req.From, dao.Debit, req.Amount,
			limitCheck{wallet: req.To, walletType: walletType, op: OpTransferIn}, req.To)
	})
	return journal, err
}

// AdjustRequest is an operator's correction.
type AdjustRequest struct {
	Wallet *dao.Wallet
	Amount int64
	// Direction is Credit to add value to the wallet, Debit to remove it.
	Direction dao.Direction
	// ProductID funds the adjustment. Required for a platform wallet; for a
	// product wallet it must be that product.
	ProductID  int64
	ReasonCode string
	Memo       string
	Reference  string
}

// Adjust credits or debits a wallet against the product's adjustments
// expense. It bypasses capability and limit rules — see OpAdjust — but not
// the ledger's overdraft check: an adjustment can correct a balance, not
// create a debt the wallet type does not allow.
func (e *Engine) Adjust(ctx context.Context, req AdjustRequest) (*dao.Journal, error) {
	if !slices.Contains(AdjustReasons, req.ReasonCode) {
		return nil, apperrors.New(apperrors.InvalidArgument,
			"reason code %q is not one of %v", req.ReasonCode, AdjustReasons)
	}
	if req.Direction != dao.Credit && req.Direction != dao.Debit {
		return nil, apperrors.New(apperrors.InvalidArgument, "an adjustment must credit or debit")
	}
	if req.Wallet.ProductID != nil && *req.Wallet.ProductID != req.ProductID {
		return nil, apperrors.New(apperrors.InvalidArgument, "a product wallet is adjusted against its own product")
	}

	var journal *dao.Journal
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		walletType, err := e.repo.GetWalletTypeByID(ctx, req.Wallet.WalletTypeID)
		if err != nil {
			return err
		}
		if err := assertAllowed(walletType, req.Wallet, OpAdjust, req.Amount); err != nil {
			return err
		}
		product, err := e.repo.GetProductByID(ctx, req.ProductID)
		if err != nil {
			return err
		}
		adjustments, err := e.repo.GetLedgerAccountByCode(ctx, ledger.ProductAdjustments(product.Code))
		if err != nil {
			return err
		}

		journal = newJournal("adjust:"+req.Wallet.PublicID+":"+req.Reference, dao.JournalAdjustment, &product.ID, req.Wallet)
		journal.ReasonCode = &req.ReasonCode
		journal.Memo = req.Memo
		journal.Postings = []*dao.Posting{
			leg(adjustments.ID, -req.Direction, req.Amount, walletType.Currency),
			leg(req.Wallet.LedgerAccountID, req.Direction, req.Amount, walletType.Currency),
		}
		return e.post(ctx, journal, req.Wallet, req.Direction, req.Amount, limitCheck{})
	})
	return journal, err
}

// HoldRequest reserves wallet value for a spend not yet final.
type HoldRequest struct {
	Wallet    *dao.Wallet
	Amount    int64
	Reference string
	ExpiresAt time.Time
}

// HoldExternalID is the ledger hold id for a wallet hold reference.
func HoldExternalID(wallet *dao.Wallet, reference string) string {
	return "hold:" + wallet.PublicID + ":" + reference
}

func (e *Engine) Hold(ctx context.Context, req HoldRequest) (*dao.Hold, error) {
	hold := &dao.Hold{
		PublicID:   ids.New(ids.LedgerHold),
		ExternalID: HoldExternalID(req.Wallet, req.Reference),
		AccountID:  req.Wallet.LedgerAccountID,
		Amount:     req.Amount,
		ExpiresAt:  req.ExpiresAt,
	}
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		walletType, err := e.repo.GetWalletTypeByID(ctx, req.Wallet.WalletTypeID)
		if err != nil {
			return err
		}
		if err := assertAllowed(walletType, req.Wallet, OpHold, req.Amount); err != nil {
			return err
		}
		hold.Currency = walletType.Currency
		return e.repo.PlaceHold(ctx, hold)
	})
	return hold, err
}

// CaptureHold spends up to a hold's amount: Dr wallet, Cr the counter account,
// freeing the rest of the reservation.
func (e *Engine) CaptureHold(ctx context.Context, req SpendRequest, holdReference string) (*dao.Journal, error) {
	var journal *dao.Journal
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		walletType, err := e.repo.GetWalletTypeByID(ctx, req.Wallet.WalletTypeID)
		if err != nil {
			return err
		}
		counter, err := e.repo.GetLedgerAccountByCode(ctx, req.CounterAccountCode)
		if err != nil {
			return err
		}
		journal = newJournal(req.ExternalID, req.Kind, &req.ProductID, req.Wallet)
		journal.Postings = []*dao.Posting{
			leg(req.Wallet.LedgerAccountID, dao.Debit, req.Amount, walletType.Currency),
			leg(counter.ID, dao.Credit, req.Amount, walletType.Currency),
		}
		_, captured, err := e.repo.CaptureHold(ctx, HoldExternalID(req.Wallet, holdReference), journal)
		if err != nil || !captured {
			return err
		}
		return e.emitMovements(ctx, journal, []*dao.Wallet{req.Wallet})
	})
	return journal, err
}

func (e *Engine) ReleaseHold(ctx context.Context, wallet *dao.Wallet, holdReference string) (*dao.Hold, error) {
	var hold *dao.Hold
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		hold, err = e.repo.ReleaseHold(ctx, HoldExternalID(wallet, holdReference))
		return err
	})
	return hold, err
}

// limitCheck names the wallet whose balance-dependent limits a posting must
// respect. The zero value checks nothing.
type limitCheck struct {
	wallet     *dao.Wallet
	walletType *dao.WalletType
	op         Operation
}

// post writes the journal, checking limits under the ledger lock, and makes
// a replay honest: the same reference for a different amount or direction is
// a conflict, not a silent return of the first journal.
//
// Each wallet the journal moves gets a wallet.credited or wallet.debited
// event, recorded in the same transaction (plan.md D5): the event exists if
// and only if the money moved. others are wallets besides the primary one
// that the journal touches, such as a transfer's recipient.
func (e *Engine) post(ctx context.Context, journal *dao.Journal, wallet *dao.Wallet, direction dao.Direction, amount int64, limits limitCheck, others ...*dao.Wallet) error {
	posted, err := e.repo.PostJournalChecked(ctx, journal, func(ctx context.Context) error {
		return e.checkLimitsAfterPosting(ctx, limits)
	})
	if err != nil {
		return err
	}
	if posted {
		return e.emitMovements(ctx, journal, append([]*dao.Wallet{wallet}, others...))
	}
	for _, p := range journal.Postings {
		if p.AccountID == wallet.LedgerAccountID && p.Direction == direction && p.Amount == amount {
			return nil
		}
	}
	return apperrors.New(apperrors.AlreadyExists,
		"%q was already used for a different wallet operation", journal.ExternalID)
}

// checkLimitsAfterPosting enforces the limits that depend on the balance
// being moved. It runs with the wallet's account locked and the posting
// already written, so it sees the balance as it would commit and no
// concurrent posting can slip past it.
func (e *Engine) checkLimitsAfterPosting(ctx context.Context, c limitCheck) error {
	if c.wallet == nil || !c.op.credits() {
		return nil
	}
	walletType := c.walletType

	if walletType.MaxBalance != nil {
		balance, err := e.repo.GetBalance(ctx, c.wallet.LedgerAccountID)
		if err != nil {
			return err
		}
		if natural := balance.Natural(dao.AccountLiability); natural > *walletType.MaxBalance {
			return denied("this would take the %s wallet to %d, above its maximum balance of %d",
				walletType.Code, natural, *walletType.MaxBalance)
		}
	}

	startOfDay := e.startOfDay()
	if walletType.DailyLoadLimit != nil && c.op.loads() {
		loaded, err := e.repo.SumWalletLoadsSince(ctx, c.wallet.LedgerAccountID, startOfDay)
		if err != nil {
			return err
		}
		if loaded > *walletType.DailyLoadLimit {
			return denied("this would load %d into the %s wallet today, above its daily limit of %d",
				loaded, walletType.Code, *walletType.DailyLoadLimit)
		}
	}

	return e.checkCustomerLimits(ctx, c, startOfDay)
}

// checkCustomerLimits enforces the per-person caps. They span wallets, so the
// customer row is locked to serialise them — after the ledger locks, which
// every path takes first, so the order is always the same.
func (e *Engine) checkCustomerLimits(ctx context.Context, c limitCheck, startOfDay time.Time) error {
	if !c.walletType.Fundable || (e.limits.MaxCustomerBalance == 0 && e.limits.MaxCustomerDailyLoad == 0) {
		return nil
	}
	if err := e.repo.LockCustomer(ctx, c.wallet.CustomerID); err != nil {
		return err
	}

	if e.limits.MaxCustomerBalance > 0 {
		held, err := e.repo.CustomerFundedBalance(ctx, c.wallet.CustomerID)
		if err != nil {
			return err
		}
		if held > e.limits.MaxCustomerBalance {
			return denied("this would take the customer's balance across all wallets to %d, above the %d cap",
				held, e.limits.MaxCustomerBalance)
		}
	}
	if e.limits.MaxCustomerDailyLoad > 0 && c.op.loads() {
		loaded, err := e.repo.CustomerFundedLoadsSince(ctx, c.wallet.CustomerID, startOfDay)
		if err != nil {
			return err
		}
		if loaded > e.limits.MaxCustomerDailyLoad {
			return denied("this would load %d for the customer today across all wallets, above the %d cap",
				loaded, e.limits.MaxCustomerDailyLoad)
		}
	}
	return nil
}

func (e *Engine) startOfDay() time.Time {
	now := e.now().In(ist)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, ist)
}

// ErrExpiryStale is returned when a wallet moved between being found dormant
// and being expired. Nothing is posted; the next sweep looks again.
var ErrExpiryStale = apperrors.New(apperrors.FailedPrecondition, "wallet moved since it was found dormant")

// Expire lapses a dormant wallet's whole balance (plan.md D10):
//
//   - purchased value (a fundable type) becomes breakage income — the customer
//     paid and did not use it;
//   - granted value goes back against the promotions expense it came from, so
//     promotions report what was actually consumed, not what was issued.
//
// Keyed on the last posting seen, so a dormant period lapses at most once.
// Under the ledger lock it confirms nothing else moved and the balance is now
// exactly zero; otherwise it refuses with ErrExpiryStale and posts nothing.
func (e *Engine) Expire(ctx context.Context, candidate dao.ExpiryCandidate) (*dao.Journal, error) {
	var journal *dao.Journal
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		w, err := e.repo.GetWalletByPublicID(ctx, candidate.WalletPublicID)
		if err != nil {
			return err
		}
		if w.ProductID == nil {
			return apperrors.New(apperrors.FailedPrecondition, "platform wallet %s cannot expire", w.PublicID)
		}
		walletType, err := e.repo.GetWalletTypeByID(ctx, w.WalletTypeID)
		if err != nil {
			return err
		}
		product, err := e.repo.GetProductByID(ctx, *w.ProductID)
		if err != nil {
			return err
		}

		destination := ledger.ProductPromotions(product.Code)
		if walletType.Fundable {
			destination = ledger.ProductBreakage(product.Code)
		}
		counter, err := e.repo.GetLedgerAccountByCode(ctx, destination)
		if err != nil {
			return err
		}

		journal = newJournal("expiry:"+w.PublicID+":"+strconv.FormatInt(candidate.LastPostingID, 10),
			dao.JournalExpiry, w.ProductID, w)
		journal.Memo = "rolling expiry after " + strconv.Itoa(derefInt(walletType.ExpiryDays)) + " days without activity"
		journal.Postings = []*dao.Posting{
			leg(w.LedgerAccountID, dao.Debit, candidate.Balance, walletType.Currency),
			leg(counter.ID, dao.Credit, candidate.Balance, walletType.Currency),
		}

		posted, err := e.repo.PostJournalChecked(ctx, journal, func(ctx context.Context) error {
			latest, err := e.repo.LatestPostingID(ctx, w.LedgerAccountID, journal.ID)
			if err != nil {
				return err
			}
			balance, err := e.repo.GetBalance(ctx, w.LedgerAccountID)
			if err != nil {
				return err
			}
			if latest != candidate.LastPostingID || balance.RawBalance != 0 || balance.Held != 0 {
				return ErrExpiryStale
			}
			return nil
		})
		// A spend since the wallet was found leaves less than the balance being
		// expired, which the ledger refuses as an overdraft. That is the same
		// finding — the wallet moved — and not an error worth logging as one.
		if apperrors.Is(err, apperrors.InsufficientBalance) {
			return ErrExpiryStale
		}
		if err != nil || !posted {
			return err
		}
		return e.emitMovements(ctx, journal, []*dao.Wallet{w})
	})
	return journal, err
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// walletMovement is the payload of wallet.credited and wallet.debited.
// Amounts are minor units; balance_after is the natural balance, so a
// consumer showing "your balance is now" needs no sign conventions.
type walletMovement struct {
	WalletID     string  `json:"wallet_id"`
	CustomerID   string  `json:"customer_id"`
	Amount       int64   `json:"amount"`
	Currency     string  `json:"currency"`
	BalanceAfter int64   `json:"balance_after"`
	JournalID    string  `json:"journal_id"`
	JournalKind  string  `json:"journal_kind"`
	ExternalID   string  `json:"external_id"`
	ReasonCode   *string `json:"reason_code,omitempty"`
}

const (
	TopicWalletCredited = "wallet.credited"
	TopicWalletDebited  = "wallet.debited"
)

func (e *Engine) emitMovements(ctx context.Context, journal *dao.Journal, wallets []*dao.Wallet) error {
	for _, w := range wallets {
		for _, p := range journal.Postings {
			if p.AccountID != w.LedgerAccountID {
				continue
			}
			topic := TopicWalletCredited
			if p.Direction == dao.Debit {
				topic = TopicWalletDebited
			}
			payload, err := json.Marshal(walletMovement{
				WalletID:     w.PublicID,
				CustomerID:   w.CustomerPublicID,
				Amount:       p.Amount,
				Currency:     p.Currency,
				BalanceAfter: p.BalanceAfter * dao.AccountLiability.NormalSign(),
				JournalID:    journal.PublicID,
				JournalKind:  string(journal.Kind),
				ExternalID:   journal.ExternalID,
				ReasonCode:   journal.ReasonCode,
			})
			if err != nil {
				return apperrors.Wrap(err, apperrors.Internal, "failed to encode wallet event")
			}
			if err := e.repo.SaveOutboxEvent(ctx, &dao.OutboxEvent{
				EventID:       ids.New(ids.OutboxEvent),
				Topic:         topic,
				AggregateType: "wallet",
				AggregateID:   w.PublicID,
				Payload:       payload,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func newJournal(externalID string, kind dao.JournalKind, productID *int64, wallet *dao.Wallet) *dao.Journal {
	source := "wallet"
	return &dao.Journal{
		PublicID:   ids.New(ids.LedgerJournal),
		ExternalID: externalID,
		Kind:       kind,
		ProductID:  productID,
		SourceKind: &source,
		SourceID:   &wallet.PublicID,
	}
}

func leg(accountID int64, direction dao.Direction, amount int64, currency string) *dao.Posting {
	return &dao.Posting{AccountID: accountID, Direction: direction, Amount: amount, Currency: currency}
}

// CheckFund says whether a wallet could be funded with amount, before any
// money is collected. It checks what is knowable up front — capability,
// status, per-transaction limit — so a customer is not sent to a checkout
// for a top-up that will certainly be refused.
//
// Balance-dependent limits can still refuse the credit when the payment is
// captured; the payment engine handles that by owing the money back rather
// than losing it.
func (e *Engine) CheckFund(ctx context.Context, w *dao.Wallet, amount int64) error {
	walletType, err := e.repo.GetWalletTypeByID(ctx, w.WalletTypeID)
	if err != nil {
		return err
	}
	return assertAllowed(walletType, w, OpFund, amount)
}

// RefundOut takes a top-up back out of a wallet to be refunded to its source:
// Dr wallet, Cr the counter account (the product's refunds_payable). The
// ledger refuses it if the customer has already spent the money — a top-up
// can only be refunded while its value is still in the wallet.
func (e *Engine) RefundOut(ctx context.Context, req SpendRequest) (*dao.Journal, error) {
	return e.move(ctx, req, OpRefundOut, dao.Debit, nil)
}

// Restore returns a failed refund's money: Dr the counter account, Cr wallet,
// marked as reversing the journal that took it out.
func (e *Engine) Restore(ctx context.Context, req SpendRequest, reverses *dao.Journal) (*dao.Journal, error) {
	return e.move(ctx, req, OpRestore, dao.Credit, &reverses.ID)
}

// move posts a two-legged journal between a wallet and one counter account,
// in the direction given for the wallet.
func (e *Engine) move(ctx context.Context, req SpendRequest, op Operation, walletDirection dao.Direction, reverses *int64) (*dao.Journal, error) {
	var journal *dao.Journal
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		walletType, err := e.repo.GetWalletTypeByID(ctx, req.Wallet.WalletTypeID)
		if err != nil {
			return err
		}
		if err := assertAllowed(walletType, req.Wallet, op, req.Amount); err != nil {
			return err
		}
		counter, err := e.repo.GetLedgerAccountByCode(ctx, req.CounterAccountCode)
		if err != nil {
			return err
		}
		journal = newJournal(req.ExternalID, req.Kind, &req.ProductID, req.Wallet)
		journal.ReversesJournalID = reverses
		journal.Postings = []*dao.Posting{
			leg(req.Wallet.LedgerAccountID, walletDirection, req.Amount, walletType.Currency),
			leg(counter.ID, -walletDirection, req.Amount, walletType.Currency),
		}
		return e.post(ctx, journal, req.Wallet, walletDirection, req.Amount, limitCheck{})
	})
	return journal, err
}

// ChargebackOut takes contested money out of a wallet into the product's
// disputed account while a chargeback is decided.
func (e *Engine) ChargebackOut(ctx context.Context, req SpendRequest) (*dao.Journal, error) {
	return e.move(ctx, req, OpChargeback, dao.Debit, nil)
}

// CheckSpend says whether a wallet may pay amount towards a purchase: its
// status and per-transaction limit. The balance is the ledger's to judge.
func (e *Engine) CheckSpend(ctx context.Context, w *dao.Wallet, amount int64) error {
	walletType, err := e.repo.GetWalletTypeByID(ctx, w.WalletTypeID)
	if err != nil {
		return err
	}
	return assertAllowed(walletType, w, OpSpend, amount)
}

// EmitMovements records wallet.credited / wallet.debited events for a journal
// posted outside this engine — an order's single settlement journal, which
// moves several wallets at once. Call it in the journal's transaction.
func (e *Engine) EmitMovements(ctx context.Context, journal *dao.Journal, wallets []*dao.Wallet) error {
	return e.emitMovements(ctx, journal, wallets)
}

// CheckRefundIn says whether a wallet may receive a refund: its type must be
// refundable_to_source and the wallet active.
func (e *Engine) CheckRefundIn(ctx context.Context, w *dao.Wallet, amount int64) error {
	walletType, err := e.repo.GetWalletTypeByID(ctx, w.WalletTypeID)
	if err != nil {
		return err
	}
	return assertAllowed(walletType, w, OpRefundIn, amount)
}
