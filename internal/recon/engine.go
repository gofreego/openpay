// Package recon proves that what a PSP says happened matches what our ledger
// says happened (plan.md phase 8).
//
// A settlement mixes every product's payments into one bank credit, so
// matching is done item by item, never on batch totals: each line is matched
// to the payment, refund or dispute it settles, and carries that record's
// product through. Anything that does not match is parked in the provider's
// suspense account and put in front of a person — never guessed at.
package recon

import (
	"context"
	"sort"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/payment"
	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"

	"github.com/gofreego/goutils/logger"
)

type Repository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error

	CreateSettlement(ctx context.Context, s *dao.Settlement) (bool, error)
	SetSettlementStatus(ctx context.Context, id int64, status dao.SettlementStatus) error
	LatestSettlementAt(ctx context.Context, providerName string) (time.Time, error)
	CreateSettlementItem(ctx context.Context, it *dao.SettlementItem) error
	IsSettled(ctx context.Context, kind, providerRef string) (bool, error)

	CreateBreak(ctx context.Context, b *dao.ReconBreak) (bool, error)
	UpdateBreak(ctx context.Context, b *dao.ReconBreak) error
	OpenMissingAtProviderBreak(ctx context.Context, paymentID int64) (*dao.ReconBreak, error)
	ListUnsettledPayments(ctx context.Context, providerName string, capturedBefore time.Time, limit int) ([]*dao.Payment, error)

	GetAttemptByProviderRef(ctx context.Context, providerName, providerPaymentID string) (*dao.PaymentAttempt, error)
	GetPaymentByID(ctx context.Context, id int64) (*dao.Payment, error)
	GetRefundByProviderRef(ctx context.Context, providerName, providerRefundID string) (*dao.Refund, error)
	GetDisputeByProviderRef(ctx context.Context, providerName, providerDisputeID string) (*dao.Dispute, error)
	GetProductByID(ctx context.Context, id int64) (*dao.Product, error)
	GetLedgerAccountByCode(ctx context.Context, code string) (*dao.LedgerAccount, error)
	PostJournal(ctx context.Context, journal *dao.Journal) error
}

type Config struct {
	// Bank is the account settlements land in (a ledger.Bank code segment).
	Bank string `yaml:"Bank"`
	// SettleWithin is how long a captured payment may go unsettled before it
	// is flagged missing at the provider.
	SettleWithin time.Duration `yaml:"SettleWithin"`
}

func (c *Config) WithDefaults() {
	if c.Bank == "" {
		c.Bank = "hdfc"
	}
	if c.SettleWithin <= 0 {
		c.SettleWithin = 72 * time.Hour
	}
}

type Engine struct {
	repo      Repository
	providers *provider.Registry
	payments  *payment.Engine
	cfg       Config
	now       func() time.Time
}

func New(repo Repository, providers *provider.Registry, payments *payment.Engine, cfg Config) *Engine {
	cfg.WithDefaults()
	return &Engine{repo: repo, providers: providers, payments: payments, cfg: cfg, now: time.Now}
}

// Ingest fetches a provider's new settlements and reconciles each. Safe to
// run repeatedly: a settlement already ingested is skipped by its id.
func (e *Engine) Ingest(ctx context.Context, providerName string) (ingested int, err error) {
	p, err := e.providers.Get(providerName)
	if err != nil {
		return 0, err
	}
	since, err := e.repo.LatestSettlementAt(ctx, providerName)
	if err != nil {
		return 0, err
	}
	// A little overlap: a settlement stamped at the same instant as the last
	// one ingested must not be missed. The unique id makes re-reading harmless.
	settlements, err := p.FetchSettlements(ctx, since.Add(-time.Minute))
	if err != nil {
		return 0, err
	}
	for _, st := range settlements {
		inserted, err := e.Reconcile(ctx, providerName, st)
		if err != nil {
			return ingested, err
		}
		if inserted {
			ingested++
		}
	}
	return ingested, nil
}

// Reconcile records one settlement, matches every line, and books it in one
// journal. inserted is false when it was already ingested.
//
// For each line, what the ledger expected (E) is credited to the provider's
// receivable, and whatever the line holds beyond that — net + fee + tax − E —
// to its suspense account. So every line balances by construction, a clean
// line leaves suspense untouched, and every difference is visible there
// rather than absorbed somewhere quieter:
//
//	Dr bank:<bank>:current           the net credit
//	Dr expense:<product>:psp_fees    actual fees, per product (D12)
//	Dr asset:input_tax_credit        GST on those fees (D13)
//	Cr psp:<provider>:receivable     what the ledger expected
//	Cr psp:<provider>:suspense       what it did not
func (e *Engine) Reconcile(ctx context.Context, providerName string, st *provider.Settlement) (inserted bool, err error) {
	err = e.repo.WithTx(ctx, func(ctx context.Context) error {
		s := &dao.Settlement{
			PublicID: ids.New(ids.Settlement), Provider: providerName,
			ProviderSettlementID: st.ProviderSettlementID, Bank: e.cfg.Bank, BankReference: st.BankReference,
			SettledAt: st.SettledAt, Currency: st.Currency, ItemCount: len(st.Items), Raw: st.Raw,
			Status: dao.SettlementClean,
		}
		for _, it := range st.Items {
			s.Gross += it.Gross
			s.Fees += it.Fee
			s.FeeTax += it.FeeTax
			s.Net += it.Net
		}
		var err error
		if inserted, err = e.repo.CreateSettlement(ctx, s); err != nil || !inserted {
			return err
		}

		book := newBook()
		book.add(ledger.Bank(e.cfg.Bank), s.Net)
		seen := map[string]bool{}
		breaks := 0
		for _, line := range st.Items {
			item, err := e.match(ctx, providerName, s, line, seen)
			if err != nil {
				return err
			}
			if err := e.repo.CreateSettlementItem(ctx, item); err != nil {
				return err
			}
			if err := e.bookItem(ctx, book, providerName, item); err != nil {
				return err
			}
			if item.Classification != dao.Matched {
				breaks++
				if err := e.openBreak(ctx, providerName, s, item); err != nil {
					return err
				}
				continue
			}
			if item.PaymentID != nil {
				if err := e.settlePayment(ctx, *item.PaymentID, s.PublicID); err != nil {
					return err
				}
			}
		}

		journal, err := e.journal(ctx, book, s, providerName)
		if err != nil {
			return err
		}
		// An empty settlement — the provider paid out nothing that day — is
		// still recorded, but moves no money.
		if len(journal.Postings) > 0 {
			if err := e.repo.PostJournal(ctx, journal); err != nil {
				return err
			}
		}
		if breaks > 0 {
			logger.Warn(ctx, "settlement %s from %s: %d of %d lines did not match", st.ProviderSettlementID, providerName, breaks, len(st.Items))
			return e.repo.SetSettlementStatus(ctx, s.ID, dao.SettlementBreaks)
		}
		return nil
	})
	return inserted, err
}

// match classifies one settlement line against the ledger.
func (e *Engine) match(ctx context.Context, providerName string, s *dao.Settlement, line provider.SettlementItem, seen map[string]bool) (*dao.SettlementItem, error) {
	item := &dao.SettlementItem{
		SettlementID: s.ID, Kind: string(line.Kind), ProviderRef: line.ProviderRef,
		Gross: line.Gross, Fee: line.Fee, FeeTax: line.FeeTax, Net: line.Net,
		Classification: dao.Matched,
	}
	defer func() { item.Unexplained = item.Net + item.Fee + item.FeeTax - item.Expected }()

	key := item.Kind + ":" + item.ProviderRef
	already, err := e.repo.IsSettled(ctx, item.Kind, item.ProviderRef)
	if err != nil {
		return nil, err
	}
	if already || seen[key] {
		item.Classification = dao.Duplicate
		return item, nil
	}
	seen[key] = true

	var ledgerAmount int64
	switch line.Kind {
	case provider.SettlePayment:
		attempt, err := e.repo.GetAttemptByProviderRef(ctx, providerName, line.ProviderRef)
		if apperrors.Is(err, apperrors.NotFound) {
			item.Classification = dao.MissingInLedger
			return item, nil
		}
		if err != nil {
			return nil, err
		}
		p, err := e.repo.GetPaymentByID(ctx, attempt.PaymentID)
		if err != nil {
			return nil, err
		}
		item.PaymentID, item.ProductID = &p.ID, &p.ProductID
		if p.CapturedAmount == nil {
			// The provider settled money our ledger never saw captured.
			item.Classification = dao.MissingInLedger
			return item, nil
		}
		ledgerAmount = *p.CapturedAmount
	case provider.SettleRefund:
		r, err := e.repo.GetRefundByProviderRef(ctx, providerName, line.ProviderRef)
		if apperrors.Is(err, apperrors.NotFound) {
			item.Classification = dao.MissingInLedger
			return item, nil
		}
		if err != nil {
			return nil, err
		}
		item.RefundID, item.PaymentID, item.ProductID = &r.ID, &r.PaymentID, &r.ProductID
		if r.Status != dao.RefundProcessed {
			item.Classification = dao.MissingInLedger
			item.PaymentID = nil // the payment itself is not what settles here
			return item, nil
		}
		item.PaymentID = nil
		ledgerAmount = -r.Amount
	case provider.SettleChargeback:
		d, err := e.repo.GetDisputeByProviderRef(ctx, providerName, line.ProviderRef)
		if apperrors.Is(err, apperrors.NotFound) {
			item.Classification = dao.MissingInLedger
			return item, nil
		}
		if err != nil {
			return nil, err
		}
		item.DisputeID, item.ProductID = &d.ID, &d.ProductID
		if d.Status != dao.DisputeLost {
			item.Classification = dao.MissingInLedger
			return item, nil
		}
		ledgerAmount = -d.Amount
	default:
		item.Classification = dao.MissingInLedger
		return item, nil
	}

	item.Expected = ledgerAmount
	switch {
	case line.Gross != ledgerAmount:
		item.Classification = dao.AmountMismatch
	case line.Net+line.Fee+line.FeeTax != line.Gross:
		// The provider's own line does not add up: its fee, tax and net
		// disagree with its gross.
		item.Classification = dao.FeeMismatch
	}
	return item, nil
}

// bookItem adds a line's legs to the settlement journal.
func (e *Engine) bookItem(ctx context.Context, book *book, providerName string, item *dao.SettlementItem) error {
	feeAccount := ledger.PlatformPSPFees
	if item.ProductID != nil && item.Classification == dao.Matched {
		// Per-payment fees belong to the payment's product (D12): this is
		// where estimated fees become facts, at the finest grain.
		product, err := e.repo.GetProductByID(ctx, *item.ProductID)
		if err != nil {
			return err
		}
		feeAccount = ledger.ProductPSPFees(product.Code)
	}
	book.add(feeAccount, item.Fee)
	book.add(ledger.InputTaxCredit, item.FeeTax)
	book.add(ledger.PSPReceivable(providerName), -item.Expected)
	book.add(ledger.PSPSuspense(providerName), -item.Unexplained)
	return nil
}

func (e *Engine) openBreak(ctx context.Context, providerName string, s *dao.Settlement, item *dao.SettlementItem) error {
	_, err := e.repo.CreateBreak(ctx, &dao.ReconBreak{
		PublicID: ids.New(ids.ReconBreak), Provider: providerName, Classification: item.Classification,
		SettlementItemID: &item.ID, PaymentID: item.PaymentID, ProductID: item.ProductID,
		Amount: item.Unexplained, Currency: s.Currency, Status: dao.BreakOpen,
		Detail: string(item.Classification) + " on " + item.Kind + " " + item.ProviderRef +
			" in settlement " + s.ProviderSettlementID,
	})
	return err
}

// settlePayment marks a matched payment settled, and closes any earlier
// "missing at provider" break for it: it was only late.
func (e *Engine) settlePayment(ctx context.Context, paymentID int64, settlementRef string) error {
	if _, err := e.payments.MarkSettled(ctx, paymentID, settlementRef); err != nil {
		return err
	}
	b, err := e.repo.OpenMissingAtProviderBreak(ctx, paymentID)
	if err != nil || b == nil {
		return err
	}
	now, reason, by, note := e.now(), "settled_late", "system", "settled in "+settlementRef
	b.Status, b.ReasonCode, b.ResolvedBy, b.ResolvedAt, b.Note = dao.BreakResolved, &reason, &by, &now, &note
	return e.repo.UpdateBreak(ctx, b)
}

// FlagUnsettled opens a "missing at provider" break for every payment the
// provider captured but has not settled within SettleWithin. The money sits
// in the receivable meanwhile; the break is the prompt to ask why.
func (e *Engine) FlagUnsettled(ctx context.Context, providerName string) (flagged int, err error) {
	payments, err := e.repo.ListUnsettledPayments(ctx, providerName, e.now().Add(-e.cfg.SettleWithin), 500)
	if err != nil {
		return 0, err
	}
	for _, p := range payments {
		created, err := e.repo.CreateBreak(ctx, &dao.ReconBreak{
			PublicID: ids.New(ids.ReconBreak), Provider: providerName, Classification: dao.MissingAtProvider,
			PaymentID: &p.ID, ProductID: &p.ProductID, Amount: *p.CapturedAmount, Currency: p.Currency,
			Status: dao.BreakOpen,
			Detail: "payment " + p.PublicID + " captured " + p.CapturedAt.Format(time.RFC3339) + " but not settled",
		})
		if err != nil {
			return flagged, err
		}
		if created {
			flagged++
		}
	}
	return flagged, nil
}

// book accumulates signed amounts per account: positive debits, negative credits.
type book struct{ amounts map[string]int64 }

func newBook() *book { return &book{amounts: map[string]int64{}} }

func (b *book) add(code string, signed int64) { b.amounts[code] += signed }

func (e *Engine) journal(ctx context.Context, b *book, s *dao.Settlement, providerName string) (*dao.Journal, error) {
	codes := make([]string, 0, len(b.amounts))
	for code := range b.amounts {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	var postings []*dao.Posting
	for _, code := range codes {
		amount := b.amounts[code]
		if amount == 0 {
			continue
		}
		account, err := e.repo.GetLedgerAccountByCode(ctx, code)
		if err != nil {
			return nil, err
		}
		direction := dao.Debit
		if amount < 0 {
			direction, amount = dao.Credit, -amount
		}
		postings = append(postings, &dao.Posting{AccountID: account.ID, Direction: direction, Amount: amount, Currency: s.Currency})
	}
	source := "settlement"
	return &dao.Journal{
		PublicID: ids.New(ids.LedgerJournal), ExternalID: "settlement:" + providerName + ":" + s.ProviderSettlementID,
		Kind: dao.JournalSettlement, SourceKind: &source, SourceID: &s.PublicID,
		Memo: "settlement " + s.ProviderSettlementID + " (" + s.BankReference + ")", Postings: postings,
	}, nil
}
