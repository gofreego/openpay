package wallet

import (
	"context"
	"slices"
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
	SumWalletLoadsSince(ctx context.Context, accountID int64, since time.Time) (int64, error)

	PostJournalChecked(ctx context.Context, journal *dao.Journal, check func(ctx context.Context) error) (posted bool, err error)
	PlaceHold(ctx context.Context, hold *dao.Hold) error
	CaptureHold(ctx context.Context, holdExternalID string, journal *dao.Journal) (*dao.Hold, error)
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

type Engine struct {
	repo Repository
	now  func() time.Time
}

func New(repo Repository) *Engine {
	return &Engine{repo: repo, now: time.Now}
}

// Open returns the customer's wallet of a type, creating it and its ledger
// account on first reference. Safe to call repeatedly and concurrently.
func (e *Engine) Open(ctx context.Context, customer *dao.Customer, walletType *dao.WalletType) (*dao.Wallet, error) {
	if customer.Status != dao.CustomerActive {
		return nil, denied("customer %s is %s", customer.PublicID, customer.Status)
	}

	var wallet *dao.Wallet
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
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
			PublicID:        ids.New(ids.Wallet),
			CustomerID:      customer.ID,
			WalletTypeID:    walletType.ID,
			ProductID:       walletType.ProductID,
			LedgerAccountID: account.ID,
			Status:          dao.WalletActive,
		}
		created, err := e.repo.GetOrCreateWallet(ctx, wallet)
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
	return wallet, err
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
			limitCheck{wallet: req.To, walletType: walletType, op: OpTransferIn})
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
		_, err = e.repo.CaptureHold(ctx, HoldExternalID(req.Wallet, holdReference), journal)
		return err
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
func (e *Engine) post(ctx context.Context, journal *dao.Journal, wallet *dao.Wallet, direction dao.Direction, amount int64, limits limitCheck) error {
	posted, err := e.repo.PostJournalChecked(ctx, journal, func(ctx context.Context) error {
		return e.checkLimitsAfterPosting(ctx, limits)
	})
	if err != nil || posted {
		return err
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

	if walletType.DailyLoadLimit != nil && c.op.loads() {
		now := e.now().In(ist)
		startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, ist)
		loaded, err := e.repo.SumWalletLoadsSince(ctx, c.wallet.LedgerAccountID, startOfDay)
		if err != nil {
			return err
		}
		if loaded > *walletType.DailyLoadLimit {
			return denied("this would load %d into the %s wallet today, above its daily limit of %d",
				loaded, walletType.Code, *walletType.DailyLoadLimit)
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
