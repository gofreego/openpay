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
)

// DisputeRepository is the dispute part of the engine's Repository.
type DisputeRepository interface {
	CreateDispute(ctx context.Context, d *dao.Dispute) error
	UpdateDispute(ctx context.Context, d *dao.Dispute) error
	LockDispute(ctx context.Context, id int64) (*dao.Dispute, error)
	GetDisputeByProviderRef(ctx context.Context, providerName, providerDisputeID string) (*dao.Dispute, error)
	GetBalance(ctx context.Context, accountID int64) (*dao.Balance, error)
}

// Dispute event topics, published through the outbox to the owning product.
const (
	TopicDisputeOpened = "dispute.opened"
	TopicDisputeWon    = "dispute.won"
	TopicDisputeLost   = "dispute.lost"
)

// processDisputeEvent treats a dispute webhook as a hint: fetch the dispute,
// record it if it is new, then sync to what the provider says.
func (e *Engine) processDisputeEvent(ctx context.Context, providerName, providerDisputeID string) error {
	p, err := e.provider(providerName)
	if err != nil {
		return err
	}
	pd, err := p.FetchDispute(ctx, providerDisputeID)
	if err != nil {
		return err
	}

	d, err := e.repo.GetDisputeByProviderRef(ctx, providerName, providerDisputeID)
	if apperrors.Is(err, apperrors.NotFound) {
		d, err = e.openDispute(ctx, providerName, pd)
	}
	if err != nil {
		return err
	}
	_, err = e.SyncDispute(ctx, d.ID, pd, "webhook")
	return err
}

// openDispute records a chargeback and moves the contested money into the
// product's disputed account, where it waits for the decision.
//
// It takes, in order, and never more than the payment still has outstanding
// (captured less refunded — a dispute cannot pull unrelated money out of a
// wallet):
//
//   - what is still in the customer's wallet, for an applied top-up;
//   - what is still owed back in refunds_payable, for an unapplied one;
//   - the rest from the product's chargebacks expense — money the customer
//     already spent, which is our loss unless the dispute is won.
//
// Each part is recorded on the dispute, so a win puts it back exactly.
func (e *Engine) openDispute(ctx context.Context, providerName string, pd *provider.Dispute) (*dao.Dispute, error) {
	var d *dao.Dispute
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		attempt, err := e.attemptFor(ctx, providerName, pd.ProviderPaymentID)
		if err != nil {
			return err
		}
		payment, err := e.repo.LockPayment(ctx, attempt.PaymentID)
		if err != nil {
			return err
		}
		if !canTransition(payment.Status, dao.PaymentDisputed) {
			return apperrors.New(apperrors.FailedPrecondition,
				"payment %s is %s and cannot be disputed", payment.PublicID, payment.Status)
		}
		product, err := e.repo.GetProductByID(ctx, payment.ProductID)
		if err != nil {
			return err
		}

		d = &dao.Dispute{
			PublicID: ids.New(ids.Dispute), PaymentID: payment.ID, ProductID: payment.ProductID,
			Provider: providerName, ProviderDisputeID: pd.ProviderDisputeID,
			Amount: pd.Amount, Currency: pd.Currency, Reason: pd.Reason, Status: dao.DisputeOpen,
			EvidenceDueBy: pd.EvidenceDueBy, PaymentPublicID: payment.PublicID,
		}
		outstanding := max(*payment.CapturedAmount-payment.RefundedAmount, 0)
		remaining := pd.Amount

		var w *dao.Wallet
		switch payment.Application {
		case dao.ApplicationApplied:
			if w, err = e.repo.GetWalletByPublicID(ctx, *payment.WalletPublicID); err != nil {
				return err
			}
			balance, err := e.repo.GetBalance(ctx, w.LedgerAccountID)
			if err != nil {
				return err
			}
			d.FromWallet = min(remaining, outstanding, max(balance.Available(dao.AccountLiability), 0))
		case dao.ApplicationUnapplied:
			d.FromUnapplied = min(remaining, outstanding)
		}
		remaining -= d.FromWallet + d.FromUnapplied
		d.FromExpense = remaining

		if err := e.repo.CreateDispute(ctx, d); err != nil {
			return err
		}

		disputed := ledger.ProductDisputed(product.Code)
		if d.FromWallet > 0 {
			if _, err := e.wallets.ChargebackOut(ctx, wallet.SpendRequest{
				Wallet: w, Amount: d.FromWallet, CounterAccountCode: disputed,
				ProductID: payment.ProductID, Kind: dao.JournalAdjustment, ExternalID: disputeLeg(d, "wallet"),
			}); err != nil {
				return err
			}
		}
		if d.FromUnapplied > 0 {
			if err := e.postBetween(ctx, disputeLeg(d, "unapplied"), dao.JournalAdjustment, payment,
				ledger.ProductRefundsPayable(product.Code), disputed, d.FromUnapplied, d.Currency,
				"chargeback opened: owed-back money held", nil); err != nil {
				return err
			}
		}
		if d.FromExpense > 0 {
			if err := e.postBetween(ctx, disputeLeg(d, "expense"), dao.JournalAdjustment, payment,
				ledger.ProductChargebacks(product.Code), disputed, d.FromExpense, d.Currency,
				"chargeback opened: customer had already spent this", nil); err != nil {
				return err
			}
		}

		if err := e.transition(ctx, payment, dao.PaymentDisputed, "webhook", d.PublicID, "chargeback: "+pd.Reason); err != nil {
			return err
		}
		return e.emitDispute(ctx, d, TopicDisputeOpened)
	})
	return d, err
}

func disputeLeg(d *dao.Dispute, part string) string {
	return "dispute:" + d.PublicID + ":" + part
}

// SyncDispute brings a dispute in line with the provider. Only a decision
// touches the ledger:
//
//   - lost: the PSP keeps the money — Dr disputed, Cr the receivable. What
//     came from the chargebacks expense stays there as the loss;
//   - won: every part goes back where it came from — the wallet (reversing
//     the journal that took it), refunds_payable, or the expense.
func (e *Engine) SyncDispute(ctx context.Context, disputeID int64, pd *provider.Dispute, source string) (*dao.Dispute, error) {
	var d *dao.Dispute
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		if d, err = e.repo.LockDispute(ctx, disputeID); err != nil {
			return err
		}
		if d.Status.IsResolved() {
			return nil
		}

		switch pd.Status {
		case provider.DisputeOpen, provider.DisputeUnderReview:
			if status := dao.DisputeStatus(pd.Status); status != d.Status {
				d.Status = status
				return e.repo.UpdateDispute(ctx, d)
			}
			return nil
		case provider.DisputeWon, provider.DisputeLost:
			return e.resolveDispute(ctx, d, pd.Status == provider.DisputeWon, source)
		default:
			return apperrors.New(apperrors.Internal, "unknown provider dispute status %q", pd.Status)
		}
	})
	return d, err
}

func (e *Engine) resolveDispute(ctx context.Context, d *dao.Dispute, won bool, source string) error {
	payment, err := e.repo.LockPayment(ctx, d.PaymentID)
	if err != nil {
		return err
	}
	product, err := e.repo.GetProductByID(ctx, d.ProductID)
	if err != nil {
		return err
	}
	disputed := ledger.ProductDisputed(product.Code)

	to, topic := dao.PaymentDisputeLost, TopicDisputeLost
	if won {
		to, topic = dao.PaymentDisputeWon, TopicDisputeWon
		if err := e.returnDisputedFunds(ctx, d, payment, disputed); err != nil {
			return err
		}
	} else if err := e.postBetween(ctx, disputeLeg(d, "lost"), dao.JournalAdjustment, payment,
		disputed, ledger.PSPReceivable(d.Provider), d.Amount, d.Currency, "chargeback lost", nil); err != nil {
		return err
	}

	now := e.now()
	d.Status, d.ResolvedAt = dao.DisputeLost, &now
	if won {
		d.Status = dao.DisputeWon
	}
	if err := e.repo.UpdateDispute(ctx, d); err != nil {
		return err
	}
	if err := e.transition(ctx, payment, to, source, d.PublicID, ""); err != nil {
		return err
	}
	return e.emitDispute(ctx, d, topic)
}

func (e *Engine) returnDisputedFunds(ctx context.Context, d *dao.Dispute, payment *dao.Payment, disputed string) error {
	if d.FromWallet > 0 {
		taken, err := e.repo.GetJournalByExternalID(ctx, disputeLeg(d, "wallet"))
		if err != nil {
			return err
		}
		w, err := e.repo.GetWalletByPublicID(ctx, *payment.WalletPublicID)
		if err != nil {
			return err
		}
		if _, err := e.wallets.Restore(ctx, wallet.SpendRequest{
			Wallet: w, Amount: d.FromWallet, CounterAccountCode: disputed,
			ProductID: payment.ProductID, Kind: dao.JournalReversal, ExternalID: disputeLeg(d, "won:wallet"),
		}, taken); err != nil {
			return err
		}
	}
	product, err := e.repo.GetProductByID(ctx, d.ProductID)
	if err != nil {
		return err
	}
	returns := []struct {
		part, code string
		amount     int64
	}{
		{"unapplied", ledger.ProductRefundsPayable(product.Code), d.FromUnapplied},
		{"expense", ledger.ProductChargebacks(product.Code), d.FromExpense},
	}
	for _, r := range returns {
		if r.amount == 0 {
			continue
		}
		taken, err := e.repo.GetJournalByExternalID(ctx, disputeLeg(d, r.part))
		if err != nil {
			return err
		}
		if err := e.postBetween(ctx, disputeLeg(d, "won:"+r.part), dao.JournalReversal, payment,
			disputed, r.code, r.amount, d.Currency, "chargeback won", &taken.ID); err != nil {
			return err
		}
	}
	return nil
}

// SubmitDisputeEvidence answers a chargeback. Only an open dispute takes
// evidence; the provider moves it under review.
func (e *Engine) SubmitDisputeEvidence(ctx context.Context, disputeID int64, evidence, submittedBy string) (*dao.Dispute, error) {
	var d *dao.Dispute
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		if d, err = e.repo.LockDispute(ctx, disputeID); err != nil {
			return err
		}
		if d.Status != dao.DisputeOpen {
			return apperrors.New(apperrors.FailedPrecondition, "dispute %s is %s and takes no more evidence", d.PublicID, d.Status)
		}
		p, err := e.provider(d.Provider)
		if err != nil {
			return err
		}
		if err := p.SubmitDisputeEvidence(ctx, d.ProviderDisputeID, evidence); err != nil {
			return err
		}
		now := e.now()
		d.Evidence, d.EvidenceSubmittedBy, d.EvidenceSubmittedAt = &evidence, &submittedBy, &now
		d.Status = dao.DisputeUnderReview
		return e.repo.UpdateDispute(ctx, d)
	})
	return d, err
}

// PollDispute fetches an unresolved dispute, for when its webhook never came.
func (e *Engine) PollDispute(ctx context.Context, d *dao.Dispute) (*dao.Dispute, error) {
	p, err := e.provider(d.Provider)
	if err != nil {
		return nil, err
	}
	pd, err := p.FetchDispute(ctx, d.ProviderDisputeID)
	if err != nil {
		return nil, err
	}
	return e.SyncDispute(ctx, d.ID, pd, "poller")
}

type disputeEvent struct {
	DisputeID     string     `json:"dispute_id"`
	PaymentID     string     `json:"payment_id"`
	Amount        int64      `json:"amount"`
	Currency      string     `json:"currency"`
	Reason        string     `json:"reason"`
	Status        string     `json:"status"`
	EvidenceDueBy *time.Time `json:"evidence_due_by,omitempty"`
}

func (e *Engine) emitDispute(ctx context.Context, d *dao.Dispute, topic string) error {
	payload, err := json.Marshal(disputeEvent{
		DisputeID: d.PublicID, PaymentID: d.PaymentPublicID, Amount: d.Amount, Currency: d.Currency,
		Reason: d.Reason, Status: string(d.Status), EvidenceDueBy: d.EvidenceDueBy,
	})
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to encode dispute event")
	}
	return e.repo.SaveOutboxEvent(ctx, &dao.OutboxEvent{
		EventID: ids.New(ids.OutboxEvent), Topic: topic,
		AggregateType: "dispute", AggregateID: d.PublicID, Payload: payload,
	})
}
