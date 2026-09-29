// Package withdrawal cashes withdrawable wallets out to a customer's bank
// account or UPI id (plan.md phase 9).
//
// Only wallet types configured withdrawable take part, and enabling that is
// itself a compliance decision (D10, Q1). Each type's policy decides the
// rest: a minimum withdrawal, and a threshold above which a person other than
// the requester must approve it before any money leaves.
package withdrawal

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/payment"
	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/internal/wallet"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/fieldcrypt"
	"github.com/gofreego/openpay/pkg/ids"

	"github.com/gofreego/goutils/logger"
)

type Repository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error

	CreateBeneficiary(ctx context.Context, b *dao.Beneficiary) error
	GetBeneficiaryByID(ctx context.Context, id int64) (*dao.Beneficiary, error)
	CreateWithdrawal(ctx context.Context, x *dao.Withdrawal) error
	UpdateWithdrawal(ctx context.Context, x *dao.Withdrawal) error
	LockWithdrawal(ctx context.Context, id int64) (*dao.Withdrawal, error)
	WithdrawnSince(ctx context.Context, customerID int64, walletID *int64, since time.Time) (amount int64, count int64, err error)
	LockCustomer(ctx context.Context, customerID int64) error
	GetWithdrawalByProviderRef(ctx context.Context, providerName, providerPayoutID string) (*dao.Withdrawal, error)

	GetWalletByPublicID(ctx context.Context, publicID string) (*dao.Wallet, error)
	GetJournalByExternalID(ctx context.Context, externalID string) (*dao.Journal, error)
	GetLedgerAccountByCode(ctx context.Context, code string) (*dao.LedgerAccount, error)
	PostJournal(ctx context.Context, journal *dao.Journal) error
	SaveOutboxEvent(ctx context.Context, event *dao.OutboxEvent) error
	ListProviderControls(ctx context.Context) (map[string]provider.Control, error)
}

type Config struct {
	// Bank is the account payouts leave from (a ledger.Bank code segment).
	Bank string `yaml:"Bank"`
	// BeneficiaryCooling is how long a newly verified destination must wait
	// before it can receive money — time for a customer to notice a
	// destination they did not add.
	BeneficiaryCooling time.Duration `yaml:"BeneficiaryCooling"`

	// Per-person caps across every wallet a customer holds, per Indian day.
	// Zero means no cap. If a PPI licence is in scope (Q1), its limits are
	// per person, which is why these exist beside each type's own limit.
	MaxCustomerDailyWithdrawal  int64 `yaml:"MaxCustomerDailyWithdrawal"`
	MaxCustomerDailyWithdrawals int64 `yaml:"MaxCustomerDailyWithdrawals"`
}

func (c *Config) WithDefaults() {
	if c.Bank == "" {
		c.Bank = "hdfc"
	}
}

type Engine struct {
	cipher    *fieldcrypt.Cipher
	repo      Repository
	providers *provider.Registry
	wallets   *wallet.Engine
	cfg       Config
	now       func() time.Time
}

// New builds the engine and connects it to the payment engine, which hands
// it payout webhooks.
func New(repo Repository, providers *provider.Registry, wallets *wallet.Engine, payments *payment.Engine,
	cipher *fieldcrypt.Cipher, cfg Config) *Engine {
	cfg.WithDefaults()
	e := &Engine{repo: repo, providers: providers, wallets: wallets, cipher: cipher, cfg: cfg, now: time.Now}
	payments.SetPayoutHook(e)
	return e
}

func (e *Engine) provider(ctx context.Context) (provider.Provider, error) {
	controls, err := e.repo.ListProviderControls(ctx)
	if err != nil {
		return nil, err
	}
	p, _, err := e.providers.Choose(controls)
	return p, err
}

// AddBeneficiary registers where a customer's withdrawals may go, verifying
// it with the provider first: a penny drop for a bank account, a directory
// lookup for a UPI id. A destination that fails is recorded as failed, so the
// attempt is on file, and can never receive money.
func (e *Engine) AddBeneficiary(ctx context.Context, b *dao.Beneficiary) (*dao.Beneficiary, error) {
	p, err := e.provider(ctx)
	if err != nil {
		return nil, err
	}
	d := provider.Destination{Name: b.Name}
	if b.AccountNumber != nil {
		d.AccountNumber, d.IFSC = *b.AccountNumber, *b.IFSC
	}
	if b.VPA != nil {
		d.VPA = *b.VPA
	}
	v, err := p.VerifyDestination(ctx, d)
	if err != nil {
		return nil, err
	}
	b.PublicID = ids.New(ids.Beneficiary)
	if b.AccountNumber != nil {
		// Stored sealed: a database dump must not hand over bank accounts.
		plain := *b.AccountNumber
		sealed, err := e.cipher.Seal(plain)
		if err != nil {
			return nil, err
		}
		last4, fingerprint := plain[max(len(plain)-4, 0):], e.cipher.Fingerprint(plain+"|"+deref(b.IFSC))
		b.AccountNumber, b.AccountLast4, b.AccountFingerprint = &sealed, &last4, &fingerprint
	}
	if v.Verified {
		now := e.now()
		b.Status, b.VerifiedAt, b.NameAtBank = dao.BeneficiaryVerified, &now, &v.NameAtBank
	} else {
		b.Status, b.FailureReason = dao.BeneficiaryFailed, &v.FailureReason
	}
	return b, e.repo.CreateBeneficiary(ctx, b)
}

type Request struct {
	Wallet      *dao.Wallet
	Beneficiary *dao.Beneficiary
	Amount      int64
	// RequestedBy is who asked: an operator's id, or a product backend's
	// credential. Whoever it is cannot approve it.
	RequestedBy string
}

// Request starts a withdrawal, in the caller's transaction.
//
// The money leaves the wallet at once, into payouts in transit — so it cannot
// be spent while the withdrawal waits, and nothing depends on a hold outliving
// an approver's holiday. The wallet's type decides the rest: the capability
// guard refuses a closed-loop type, a frozen wallet, or an amount below the
// type's minimum; an amount above its approval threshold waits for a person;
// anything else is sent to the provider now.
func (e *Engine) Request(ctx context.Context, req Request) (*dao.Withdrawal, error) {
	b := req.Beneficiary
	if b.CustomerID != req.Wallet.CustomerID {
		return nil, apperrors.New(apperrors.NotFound, "beneficiary %q not found", b.PublicID)
	}
	if b.Status != dao.BeneficiaryVerified {
		return nil, apperrors.New(apperrors.FailedPrecondition, "beneficiary %s is %s, not verified", b.PublicID, b.Status)
	}
	if ready := b.VerifiedAt.Add(e.cfg.BeneficiaryCooling); e.now().Before(ready) {
		return nil, apperrors.New(apperrors.FailedPrecondition,
			"beneficiary %s was added recently and can receive money from %s", b.PublicID, ready.Format(time.RFC3339))
	}
	walletType, err := e.wallets.WalletType(ctx, req.Wallet)
	if err != nil {
		return nil, err
	}
	p, err := e.provider(ctx)
	if err != nil {
		return nil, err
	}

	x := &dao.Withdrawal{
		PublicID: ids.New(ids.Payout), WalletID: req.Wallet.ID, CustomerID: req.Wallet.CustomerID,
		ProductID: req.Wallet.ProductID, BeneficiaryID: b.ID, Amount: req.Amount, Currency: walletType.Currency,
		Status: dao.WithdrawalApproved, RequiresApproval: wallet.NeedsApproval(walletType, req.Amount),
		RequestedBy: req.RequestedBy, Provider: p.Name(),
		WalletPublicID: req.Wallet.PublicID, BeneficiaryPublicID: b.PublicID,
	}
	if x.RequiresApproval {
		x.Status = dao.WithdrawalPendingApproval
	}

	err = e.repo.WithTx(ctx, func(ctx context.Context) error {
		if err := e.repo.CreateWithdrawal(ctx, x); err != nil {
			return err
		}
		if _, err := e.wallets.WithdrawOut(ctx, wallet.SpendRequest{
			Wallet: req.Wallet, Amount: x.Amount, CounterAccountCode: ledger.PayoutsInTransit,
			ProductID: productOf(x), Kind: dao.JournalAdjustment, ExternalID: debitID(x),
		}); err != nil {
			return err
		}
		if err := e.checkDailyLimits(ctx, x, walletType); err != nil {
			return err
		}
		if err := e.emit(ctx, x, "withdrawal.requested"); err != nil {
			return err
		}
		if x.RequiresApproval {
			logger.Info(ctx, "withdrawal %s of %d awaits approval (above the %s threshold)", x.PublicID, x.Amount, walletType.Code)
			return nil
		}
		return e.submit(ctx, x)
	})
	return x, err
}

// Approve sends a withdrawal that waited for a person. The approver must not
// be whoever requested it — the database refuses that too.
func (e *Engine) Approve(ctx context.Context, withdrawalID int64, approver, note string) (*dao.Withdrawal, error) {
	return e.decide(ctx, withdrawalID, approver, note, func(ctx context.Context, x *dao.Withdrawal) error {
		x.Status = dao.WithdrawalApproved
		return e.submit(ctx, x)
	})
}

// Reject refuses a withdrawal that waited for a person, returning its money
// to the wallet.
func (e *Engine) Reject(ctx context.Context, withdrawalID int64, approver, note string) (*dao.Withdrawal, error) {
	return e.decide(ctx, withdrawalID, approver, note, func(ctx context.Context, x *dao.Withdrawal) error {
		return e.returnFunds(ctx, x, dao.WithdrawalRejected, "rejected: "+note, ledger.PayoutsInTransit)
	})
}

func (e *Engine) decide(ctx context.Context, withdrawalID int64, approver, note string, apply func(ctx context.Context, x *dao.Withdrawal) error) (*dao.Withdrawal, error) {
	if note == "" {
		return nil, apperrors.New(apperrors.InvalidArgument, "a decision must say why")
	}
	var x *dao.Withdrawal
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		if x, err = e.repo.LockWithdrawal(ctx, withdrawalID); err != nil {
			return err
		}
		if x.Status != dao.WithdrawalPendingApproval {
			return apperrors.New(apperrors.FailedPrecondition, "withdrawal %s is %s, not awaiting approval", x.PublicID, x.Status)
		}
		if approver == x.RequestedBy {
			return apperrors.New(apperrors.PermissionDenied, "a withdrawal cannot be approved or rejected by whoever requested it")
		}
		now := e.now()
		x.DecidedBy, x.DecidedAt, x.DecisionNote = &approver, &now, &note
		// Record the decision before acting on it. The four-eyes CHECK then
		// refuses a self-approval before any money is sent — a check that only
		// fired after the payout call would roll back our records while the
		// provider had already paid.
		if err := e.repo.UpdateWithdrawal(ctx, x); err != nil {
			return err
		}
		if err := apply(ctx, x); err != nil {
			return err
		}
		return e.repo.UpdateWithdrawal(ctx, x)
	})
	return x, err
}

// submit asks the provider to pay out. Our withdrawal id is its idempotency
// key, so asking again after a timeout cannot pay twice. A timeout leaves the
// withdrawal approved and the poller asks again; a refusal returns the money.
func (e *Engine) submit(ctx context.Context, x *dao.Withdrawal) error {
	p, err := e.providers.Get(x.Provider)
	if err != nil {
		return err
	}
	b, err := e.repo.GetBeneficiaryByID(ctx, x.BeneficiaryID)
	if err != nil {
		return err
	}
	d := provider.Destination{Name: b.Name}
	if b.AccountNumber != nil {
		// Opened only here, only to send the payout.
		account, err := e.cipher.Open(*b.AccountNumber)
		if err != nil {
			return err
		}
		d.AccountNumber, d.IFSC = account, *b.IFSC
	}
	if b.VPA != nil {
		d.VPA = *b.VPA
	}
	payout, err := p.CreatePayout(ctx, provider.PayoutRequest{PayoutID: x.PublicID, Destination: d,
		Amount: x.Amount, Currency: x.Currency})
	if apperrors.Is(err, apperrors.Unavailable) {
		logger.Warn(ctx, "withdrawal %s: provider unavailable, left approved for the poller: %v", x.PublicID, err)
		return e.repo.UpdateWithdrawal(ctx, x)
	}
	if err != nil {
		return e.returnFunds(ctx, x, dao.WithdrawalFailed, err.Error(), ledger.PayoutsInTransit)
	}
	return e.apply(ctx, x, payout)
}

// apply moves a withdrawal to the provider's view of its payout:
//
//   - processing: nothing moves yet;
//   - paid: in transit → bank. The money has reached the customer;
//   - failed: in transit → wallet. It never left;
//   - reversed: bank → wallet. It left and bounced back — the plan's
//     warning that payouts fail after you thought they left.
func (e *Engine) apply(ctx context.Context, x *dao.Withdrawal, payout *provider.Payout) error {
	x.ProviderPayoutID = &payout.ProviderPayoutID
	switch payout.Status {
	case provider.PayoutProcessing:
		if x.Status == dao.WithdrawalApproved {
			x.Status = dao.WithdrawalProcessing
		}
		return e.repo.UpdateWithdrawal(ctx, x)
	case provider.PayoutPaid:
		if x.Status == dao.WithdrawalPaid {
			return nil
		}
		if err := e.post(ctx, x, "paid", ledger.PayoutsInTransit, ledger.Bank(e.cfg.Bank), nil); err != nil {
			return err
		}
		now := e.now()
		x.Status, x.PaidAt = dao.WithdrawalPaid, &now
		if err := e.repo.UpdateWithdrawal(ctx, x); err != nil {
			return err
		}
		return e.emit(ctx, x, "withdrawal.paid")
	case provider.PayoutFailed:
		return e.returnFunds(ctx, x, dao.WithdrawalFailed, payout.FailureReason, ledger.PayoutsInTransit)
	case provider.PayoutReversed:
		if x.Status != dao.WithdrawalPaid {
			// Reversed without ever being seen paid: book the payment first,
			// so the reversal has something to reverse.
			if err := e.apply(ctx, x, &provider.Payout{ProviderPayoutID: payout.ProviderPayoutID, Status: provider.PayoutPaid}); err != nil {
				return err
			}
		}
		return e.returnFunds(ctx, x, dao.WithdrawalReversed, payout.FailureReason, ledger.Bank(e.cfg.Bank))
	default:
		return apperrors.New(apperrors.Internal, "unknown payout status %q", payout.Status)
	}
}

// returnFunds puts a withdrawal's money back in the wallet, from wherever it
// sits — in transit, or the bank it bounced back to — with a journal that
// reverses the debit that took it out.
func (e *Engine) returnFunds(ctx context.Context, x *dao.Withdrawal, status dao.WithdrawalStatus, reason, from string) error {
	debit, err := e.repo.GetJournalByExternalID(ctx, debitID(x))
	if err != nil {
		return err
	}
	w, err := e.repo.GetWalletByPublicID(ctx, x.WalletPublicID)
	if err != nil {
		return err
	}
	if _, err := e.wallets.Restore(ctx, wallet.SpendRequest{
		Wallet: w, Amount: x.Amount, CounterAccountCode: from, ProductID: productOf(x),
		Kind: dao.JournalReversal, ExternalID: "withdrawal:" + x.PublicID + ":" + string(status),
	}, debit); err != nil {
		return err
	}
	x.Status, x.FailureReason = status, &reason
	if err := e.repo.UpdateWithdrawal(ctx, x); err != nil {
		return err
	}
	return e.emit(ctx, x, "withdrawal."+string(status))
}

func (e *Engine) post(ctx context.Context, x *dao.Withdrawal, step, debitCode, creditCode string, reverses *int64) error {
	debit, err := e.repo.GetLedgerAccountByCode(ctx, debitCode)
	if err != nil {
		return err
	}
	credit, err := e.repo.GetLedgerAccountByCode(ctx, creditCode)
	if err != nil {
		return err
	}
	source := "withdrawal"
	return e.repo.PostJournal(ctx, &dao.Journal{
		PublicID: ids.New(ids.LedgerJournal), ExternalID: "withdrawal:" + x.PublicID + ":" + step,
		Kind: dao.JournalAdjustment, ProductID: x.ProductID, SourceKind: &source, SourceID: &x.PublicID,
		ReversesJournalID: reverses, Memo: "withdrawal " + step,
		Postings: []*dao.Posting{
			{AccountID: debit.ID, Direction: dao.Debit, Amount: x.Amount, Currency: x.Currency},
			{AccountID: credit.ID, Direction: dao.Credit, Amount: x.Amount, Currency: x.Currency},
		},
	})
}

// Sync brings a withdrawal in line with the provider's payout. One way in
// for webhooks, the poller and resubmission alike.
func (e *Engine) Sync(ctx context.Context, withdrawalID int64, payout *provider.Payout) (*dao.Withdrawal, error) {
	var x *dao.Withdrawal
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		if x, err = e.repo.LockWithdrawal(ctx, withdrawalID); err != nil {
			return err
		}
		switch x.Status {
		case dao.WithdrawalFailed, dao.WithdrawalRejected, dao.WithdrawalReversed, dao.WithdrawalPendingApproval:
			return nil
		}
		return e.apply(ctx, x, payout)
	})
	return x, err
}

// ProcessPayoutEvent treats a payout webhook as a hint: fetch the payout and
// sync to it. It implements payment.PayoutHook.
func (e *Engine) ProcessPayoutEvent(ctx context.Context, providerName, providerPayoutID string) error {
	x, err := e.repo.GetWithdrawalByProviderRef(ctx, providerName, providerPayoutID)
	if apperrors.Is(err, apperrors.NotFound) {
		return apperrors.New(apperrors.Unavailable, "no withdrawal yet for %s payout %s", providerName, providerPayoutID)
	}
	if err != nil {
		return err
	}
	p, err := e.providers.Get(providerName)
	if err != nil {
		return err
	}
	payout, err := p.FetchPayout(ctx, providerPayoutID)
	if err != nil {
		return err
	}
	_, err = e.Sync(ctx, x.ID, payout)
	return err
}

// Poll recovers a withdrawal that went quiet: one still approved is
// submitted again; one processing is fetched.
func (e *Engine) Poll(ctx context.Context, x *dao.Withdrawal) (*dao.Withdrawal, error) {
	if x.Status == dao.WithdrawalApproved {
		err := e.repo.WithTx(ctx, func(ctx context.Context) error {
			locked, err := e.repo.LockWithdrawal(ctx, x.ID)
			if err != nil || locked.Status != dao.WithdrawalApproved {
				return err
			}
			x = locked
			return e.submit(ctx, locked)
		})
		return x, err
	}
	if x.ProviderPayoutID == nil {
		return x, nil
	}
	p, err := e.providers.Get(x.Provider)
	if err != nil {
		return nil, err
	}
	payout, err := p.FetchPayout(ctx, *x.ProviderPayoutID)
	if err != nil {
		return nil, err
	}
	return e.Sync(ctx, x.ID, payout)
}

// ist is the day boundary for daily limits: v1 is India-only (D11).
var ist = time.FixedZone("IST", 5*60*60+30*60)

// checkDailyLimits runs after the withdrawal is recorded and its debit
// posted, so the wallet's ledger row is locked: a concurrent request for the
// same wallet waits, then counts this one. The customer-wide caps lock the
// customer row too — after the ledger lock, the order every path uses.
func (e *Engine) checkDailyLimits(ctx context.Context, x *dao.Withdrawal, walletType *dao.WalletType) error {
	now := e.now().In(ist)
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, ist)

	if limit := walletType.DailyWithdrawalLimit; limit != nil {
		amount, _, err := e.repo.WithdrawnSince(ctx, x.CustomerID, &x.WalletID, startOfDay)
		if err != nil {
			return err
		}
		if amount > *limit {
			return apperrors.New(apperrors.WalletOperationDenied,
				"this would withdraw %d from the %s wallet today, above its daily limit of %d", amount, walletType.Code, *limit)
		}
	}

	if e.cfg.MaxCustomerDailyWithdrawal == 0 && e.cfg.MaxCustomerDailyWithdrawals == 0 {
		return nil
	}
	if err := e.repo.LockCustomer(ctx, x.CustomerID); err != nil {
		return err
	}
	amount, count, err := e.repo.WithdrawnSince(ctx, x.CustomerID, nil, startOfDay)
	if err != nil {
		return err
	}
	if cap := e.cfg.MaxCustomerDailyWithdrawal; cap > 0 && amount > cap {
		return apperrors.New(apperrors.WalletOperationDenied,
			"this would withdraw %d for the customer today across all wallets, above the %d cap", amount, cap)
	}
	if cap := e.cfg.MaxCustomerDailyWithdrawals; cap > 0 && count > cap {
		return apperrors.New(apperrors.WalletOperationDenied,
			"this would be the customer's withdrawal number %d today, above the %d allowed", count, cap)
	}
	return nil
}

func debitID(x *dao.Withdrawal) string { return "withdrawal:" + x.PublicID + ":debit" }

// productOf is the withdrawal's product for its journals; 0 for a
// platform-scoped wallet, which the wallet engine books to no product.
func productOf(x *dao.Withdrawal) int64 {
	if x.ProductID == nil {
		return 0
	}
	return *x.ProductID
}

type event struct {
	WithdrawalID  string  `json:"withdrawal_id"`
	WalletID      string  `json:"wallet_id"`
	Amount        int64   `json:"amount"`
	Currency      string  `json:"currency"`
	Status        string  `json:"status"`
	FailureReason *string `json:"failure_reason,omitempty"`
}

func (e *Engine) emit(ctx context.Context, x *dao.Withdrawal, topic string) error {
	payload, err := json.Marshal(event{WithdrawalID: x.PublicID, WalletID: x.WalletPublicID, Amount: x.Amount,
		Currency: x.Currency, Status: string(x.Status), FailureReason: x.FailureReason})
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to encode withdrawal event")
	}
	return e.repo.SaveOutboxEvent(ctx, &dao.OutboxEvent{EventID: ids.New(ids.OutboxEvent), Topic: topic,
		AggregateType: "withdrawal", AggregateID: x.PublicID, Payload: payload})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// SealLegacyAccounts encrypts bank account numbers stored before encryption
// existed. Idempotent: a sealed value is never touched again. The worker
// runs it at startup, so no plaintext account number outlives a deploy.
func (e *Engine) SealLegacyAccounts(ctx context.Context, repo interface {
	ListPlaintextBeneficiaries(ctx context.Context) ([]*dao.Beneficiary, error)
	SealBeneficiaryAccount(ctx context.Context, id int64, sealed, last4, fingerprint string) error
}) (int, error) {
	legacy, err := repo.ListPlaintextBeneficiaries(ctx)
	if err != nil {
		return 0, err
	}
	for _, b := range legacy {
		plain := *b.AccountNumber
		sealed, err := e.cipher.Seal(plain)
		if err != nil {
			return 0, err
		}
		if err := repo.SealBeneficiaryAccount(ctx, b.ID, sealed, plain[max(len(plain)-4, 0):],
			e.cipher.Fingerprint(plain+"|"+deref(b.IFSC))); err != nil {
			return 0, err
		}
	}
	return len(legacy), nil
}
