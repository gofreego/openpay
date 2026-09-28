package recon

import (
	"context"
	"slices"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

// ResolutionReasons is the fixed list a break resolution must cite (U-D7).
var ResolutionReasons = []string{"provider_error", "reference_mismatch", "timing", "bank_adjustment", "unrecoverable", "other"}

// Resolution is an operator's decision on a break.
type Resolution struct {
	BreakID    string
	ReasonCode string
	Note       string
	By         string
	// PaymentID names the payment a force-match attributes the money to.
	PaymentID string
}

// Resolve closes a break by explanation alone. Only a break with nothing in
// suspense can be explained away: money that sits there must be moved by
// force-match or write-off, on the record.
func (e *Engine) Resolve(ctx context.Context, r Resolution) (*dao.ReconBreak, error) {
	return e.decide(ctx, r, func(ctx context.Context, b *dao.ReconBreak) error {
		if b.Classification != dao.MissingAtProvider && b.Amount != 0 {
			return apperrors.New(apperrors.FailedPrecondition,
				"break %s holds %d in suspense; force-match or write it off instead", b.PublicID, b.Amount)
		}
		b.Status = dao.BreakResolved
		return nil
	})
}

// ForceMatch attributes a settlement line's unexplained money to a payment an
// operator has identified — the provider sent a reference our matching
// could not follow. The money moves from suspense to the receivable, and the
// payment is marked settled.
func (e *Engine) ForceMatch(ctx context.Context, r Resolution) (*dao.ReconBreak, error) {
	return e.decide(ctx, r, func(ctx context.Context, b *dao.ReconBreak) error {
		if b.SettlementItemID == nil {
			return apperrors.New(apperrors.FailedPrecondition, "only a settlement line can be force-matched")
		}
		if r.PaymentID == "" {
			return apperrors.New(apperrors.InvalidArgument, "force-match needs the payment the money belongs to")
		}
		p, err := e.repo.GetPaymentByPublicID(ctx, r.PaymentID)
		if err != nil {
			return err
		}
		if p.Provider == nil || *p.Provider != b.Provider {
			return apperrors.New(apperrors.InvalidArgument, "payment %s was not taken by %s", p.PublicID, b.Provider)
		}
		if err := e.move(ctx, b, "force_match", ledger.PSPSuspense(b.Provider), ledger.PSPReceivable(b.Provider), -b.Amount); err != nil {
			return err
		}
		if _, err := e.payments.MarkSettled(ctx, p.ID, "force-match "+b.PublicID); err != nil {
			return err
		}
		b.Status = dao.BreakForceMatched
		return nil
	})
}

// WriteOff gives up on a break's money, on the record: what sits in suspense
// for a settlement line, or what sits unsettled in the receivable for a
// payment the provider never paid out, goes to the write-offs expense.
func (e *Engine) WriteOff(ctx context.Context, r Resolution) (*dao.ReconBreak, error) {
	return e.decide(ctx, r, func(ctx context.Context, b *dao.ReconBreak) error {
		if b.Classification == dao.MissingAtProvider {
			// The receivable expected this money; it is not coming.
			if err := e.move(ctx, b, "write_off", ledger.PSPReceivable(b.Provider), ledger.ReconWriteoffs, b.Amount); err != nil {
				return err
			}
		} else if err := e.move(ctx, b, "write_off", ledger.PSPSuspense(b.Provider), ledger.ReconWriteoffs, -b.Amount); err != nil {
			return err
		}
		b.Status = dao.BreakWrittenOff
		return nil
	})
}

// decide runs one resolution: an open break, a listed reason, an explanation.
func (e *Engine) decide(ctx context.Context, r Resolution, apply func(ctx context.Context, b *dao.ReconBreak) error) (*dao.ReconBreak, error) {
	if !slices.Contains(ResolutionReasons, r.ReasonCode) {
		return nil, apperrors.New(apperrors.InvalidArgument, "reason code %q is not one of %v", r.ReasonCode, ResolutionReasons)
	}
	if r.Note == "" {
		return nil, apperrors.New(apperrors.InvalidArgument, "a resolution must say what was found")
	}
	var b *dao.ReconBreak
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		if b, err = e.repo.LockBreak(ctx, r.BreakID); err != nil {
			return err
		}
		if b.Status != dao.BreakOpen {
			return apperrors.New(apperrors.FailedPrecondition, "break %s is already %s", b.PublicID, b.Status)
		}
		if err := apply(ctx, b); err != nil {
			return err
		}
		now := e.now()
		b.ReasonCode, b.Note, b.ResolvedBy, b.ResolvedAt = &r.ReasonCode, &r.Note, &r.By, &now
		return e.repo.UpdateBreak(ctx, b)
	})
	return b, err
}

// move posts a two-legged journal of amount from one account's side to the
// other: positive debits to, credits from; negative the reverse.
func (e *Engine) move(ctx context.Context, b *dao.ReconBreak, action, from, to string, amount int64) error {
	if amount == 0 {
		return nil
	}
	fromAccount, err := e.repo.GetLedgerAccountByCode(ctx, from)
	if err != nil {
		return err
	}
	toAccount, err := e.repo.GetLedgerAccountByCode(ctx, to)
	if err != nil {
		return err
	}
	debit, credit := toAccount.ID, fromAccount.ID
	if amount < 0 {
		debit, credit, amount = credit, debit, -amount
	}
	source, reason := "recon_break", "recon_"+action
	return e.repo.PostJournal(ctx, &dao.Journal{
		PublicID: ids.New(ids.LedgerJournal), ExternalID: "recon:" + b.PublicID + ":" + action,
		Kind: dao.JournalAdjustment, ProductID: b.ProductID, SourceKind: &source, SourceID: &b.PublicID,
		ReasonCode: &reason, Memo: action + " of " + string(b.Classification) + " break " + b.PublicID,
		Postings: []*dao.Posting{
			{AccountID: debit, Direction: dao.Debit, Amount: amount, Currency: b.Currency},
			{AccountID: credit, Direction: dao.Credit, Amount: amount, Currency: b.Currency},
		},
	})
}
