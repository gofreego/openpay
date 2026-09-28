package payment

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/internal/wallet"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"

	"github.com/gofreego/goutils/logger"
)

// RefundRepository is the refund part of the engine's Repository.
type RefundRepository interface {
	CreateRefund(ctx context.Context, refund *dao.Refund) error
	UpdateRefund(ctx context.Context, refund *dao.Refund) error
	LockRefund(ctx context.Context, id int64) (*dao.Refund, error)
	GetRefundByProviderRef(ctx context.Context, providerName, providerRefundID string) (*dao.Refund, error)
	SumProcessedRefunds(ctx context.Context, paymentID int64) (int64, error)
	GetJournalByExternalID(ctx context.Context, externalID string) (*dao.Journal, error)
}

// RefundReasons is the fixed list a refund must cite (plan.md U-D7).
var RefundReasons = []string{"customer_request", "duplicate_payment", "fraud", "service_issue", "unapplied_payment"}

// Refund event topics, published through the outbox to the owning product.
const (
	TopicRefundProcessed = "refund.processed"
	TopicRefundFailed    = "refund.failed"
)

// RefundRequest returns part or all of a captured payment to its source.
type RefundRequest struct {
	PaymentID   int64
	Amount      int64
	ReasonCode  string
	Memo        string
	RequestedBy string
}

// CreateRefund reserves the money and asks the provider to return it, in
// the caller's transaction.
//
// The money is reserved first, so it cannot be spent while the provider
// works: a top-up comes out of the wallet into refunds_payable now; a payment
// its wallet refused is already there. The payment's refunded_amount grows
// by the same figure, and the database refuses it past what was captured —
// that constraint, not a check here, is the over-refund protection.
//
// A provider that refuses outright fails the whole call, and nothing is
// kept. A provider that times out leaves the refund initiated with its money
// reserved: the provider may have it, and asking again with the same refund
// id is safe, so the poller does exactly that.
func (e *Engine) CreateRefund(ctx context.Context, req RefundRequest) (*dao.Refund, error) {
	if !slices.Contains(RefundReasons, req.ReasonCode) {
		return nil, apperrors.New(apperrors.InvalidArgument, "reason code %q is not one of %v", req.ReasonCode, RefundReasons)
	}

	var refund *dao.Refund
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		payment, err := e.repo.LockPayment(ctx, req.PaymentID)
		if err != nil {
			return err
		}
		switch payment.Status {
		case dao.PaymentCaptured, dao.PaymentSettled, dao.PaymentPartiallyRefunded:
		default:
			return apperrors.New(apperrors.FailedPrecondition, "payment %s is %s and has nothing to refund", payment.PublicID, payment.Status)
		}

		source := dao.RefundFromWallet
		switch payment.Application {
		case dao.ApplicationApplied:
			if payment.Purpose == dao.PurposeOrder {
				// The order refund has already moved this share into
				// refunds_payable, with its tax split; nothing more to reserve.
				source = dao.RefundFromOrder
			}
		case dao.ApplicationUnapplied:
			source = dao.RefundFromUnapplied
		default:
			// Suspense means the amount itself is in doubt; refunding a guess
			// compounds it. A person resolves the suspense first.
			return apperrors.New(apperrors.FailedPrecondition,
				"payment %s is in %s and must be resolved before it can be refunded", payment.PublicID, payment.Application)
		}

		refund = &dao.Refund{
			PublicID: ids.New(ids.Refund), PaymentID: payment.ID, ProductID: payment.ProductID,
			Amount: req.Amount, Currency: payment.Currency, Status: dao.RefundInitiated, Source: source,
			ReasonCode: req.ReasonCode, Memo: req.Memo, Provider: deref(payment.Provider),
			RequestedBy: req.RequestedBy, PaymentPublicID: payment.PublicID,
		}
		if err := e.repo.CreateRefund(ctx, refund); err != nil {
			return err
		}
		payment.RefundedAmount += req.Amount
		if err := e.repo.UpdatePayment(ctx, payment); err != nil {
			return err
		}

		if source == dao.RefundFromWallet {
			if err := e.reserveFromWallet(ctx, payment, refund); err != nil {
				return err
			}
		}
		return e.submitRefund(ctx, payment, refund)
	})
	return refund, err
}

func (e *Engine) reserveFromWallet(ctx context.Context, payment *dao.Payment, refund *dao.Refund) error {
	w, err := e.repo.GetWalletByPublicID(ctx, *payment.WalletPublicID)
	if err != nil {
		return err
	}
	product, err := e.repo.GetProductByID(ctx, payment.ProductID)
	if err != nil {
		return err
	}
	_, err = e.wallets.RefundOut(ctx, wallet.SpendRequest{
		Wallet: w, Amount: refund.Amount, CounterAccountCode: ledger.ProductRefundsPayable(product.Code),
		ProductID: payment.ProductID, Kind: dao.JournalRefund, ExternalID: reserveID(refund),
	})
	return err
}

func reserveID(refund *dao.Refund) string   { return "refund:" + refund.PublicID + ":reserve" }
func processedID(refund *dao.Refund) string { return "refund:" + refund.PublicID + ":processed" }
func restoreID(refund *dao.Refund) string   { return "refund:" + refund.PublicID + ":restore" }

// submitRefund asks the provider, or asks again: our refund id is the
// provider's idempotency key, so a second ask returns the first refund.
func (e *Engine) submitRefund(ctx context.Context, payment *dao.Payment, refund *dao.Refund) error {
	attempt, err := e.latestAttempt(ctx, payment)
	if err != nil {
		return err
	}
	if attempt == nil || attempt.ProviderPaymentID == nil {
		return apperrors.New(apperrors.FailedPrecondition, "payment %s has no provider payment to refund", payment.PublicID)
	}
	p, err := e.provider(refund.Provider)
	if err != nil {
		return err
	}

	pr, err := p.Refund(ctx, provider.RefundRequest{
		RefundID: refund.PublicID, ProviderPaymentID: *attempt.ProviderPaymentID,
		Amount: refund.Amount, Currency: refund.Currency,
	})
	if apperrors.Is(err, apperrors.Unavailable) {
		logger.Warn(ctx, "refund %s: provider unavailable, left initiated for the poller: %v", refund.PublicID, err)
		return nil
	}
	if err != nil {
		return err
	}
	return e.applyRefund(ctx, refund, pr, "api")
}

// SyncRefund brings a refund in line with the provider's view of it. Like
// Sync for payments, it is the one way a refund advances after it starts.
func (e *Engine) SyncRefund(ctx context.Context, refundID int64, pr *provider.Refund, source string) (*dao.Refund, error) {
	var refund *dao.Refund
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		if refund, err = e.repo.LockRefund(ctx, refundID); err != nil {
			return err
		}
		return e.applyRefund(ctx, refund, pr, source)
	})
	return refund, err
}

// applyRefund moves a refund to the provider's state. Only the move to a
// final state touches the ledger:
//
//   - processed: the money has left — Dr refunds_payable, Cr the provider's
//     receivable — and the payment becomes partially_refunded or refunded;
//   - failed: nothing left — a wallet reservation is returned to the wallet,
//     reversing the journal that took it, and refunded_amount is released.
func (e *Engine) applyRefund(ctx context.Context, refund *dao.Refund, pr *provider.Refund, source string) error {
	if refund.Status.IsFinal() {
		return nil
	}
	refund.ProviderRefundID = &pr.ProviderRefundID

	switch pr.Status {
	case provider.RefundPending:
		refund.Status = dao.RefundPending
		return e.repo.UpdateRefund(ctx, refund)
	case provider.RefundProcessed:
		return e.completeRefund(ctx, refund, pr, source)
	case provider.RefundFailed:
		return e.failRefund(ctx, refund, pr.FailureReason)
	default:
		return apperrors.New(apperrors.Internal, "unknown provider refund status %q", pr.Status)
	}
}

func (e *Engine) completeRefund(ctx context.Context, refund *dao.Refund, pr *provider.Refund, source string) error {
	if pr.Amount != refund.Amount || pr.Currency != refund.Currency {
		// The provider returned a different sum than we reserved. Booking
		// either figure would be a guess; leave it for a person, loudly.
		return apperrors.New(apperrors.FailedPrecondition,
			"refund %s: provider reports %d %s, we asked for %d %s", refund.PublicID, pr.Amount, pr.Currency, refund.Amount, refund.Currency)
	}

	payment, err := e.repo.LockPayment(ctx, refund.PaymentID)
	if err != nil {
		return err
	}
	product, err := e.repo.GetProductByID(ctx, refund.ProductID)
	if err != nil {
		return err
	}
	if err := e.postBetween(ctx, processedID(refund), dao.JournalRefund, payment,
		ledger.ProductRefundsPayable(product.Code), ledger.PSPReceivable(refund.Provider),
		refund.Amount, refund.Currency, "refund processed by "+refund.Provider, nil); err != nil {
		return err
	}

	now := e.now()
	refund.Status, refund.ProcessedAt = dao.RefundProcessed, &now
	if err := e.repo.UpdateRefund(ctx, refund); err != nil {
		return err
	}

	processed, err := e.repo.SumProcessedRefunds(ctx, payment.ID)
	if err != nil {
		return err
	}
	to := dao.PaymentPartiallyRefunded
	if payment.CapturedAmount != nil && processed >= *payment.CapturedAmount {
		to = dao.PaymentRefunded
	}
	if err := e.transition(ctx, payment, to, source, refund.PublicID, "refund processed"); err != nil {
		return err
	}
	return e.emitRefund(ctx, refund, TopicRefundProcessed)
}

func (e *Engine) failRefund(ctx context.Context, refund *dao.Refund, reason string) error {
	payment, err := e.repo.LockPayment(ctx, refund.PaymentID)
	if err != nil {
		return err
	}

	// An unapplied or order refund's money stays in refunds_payable: still
	// owed to the customer, awaiting another refund or an operator's decision.
	if refund.Source == dao.RefundFromWallet {
		reserve, err := e.repo.GetJournalByExternalID(ctx, reserveID(refund))
		if err != nil {
			return err
		}
		w, err := e.repo.GetWalletByPublicID(ctx, *payment.WalletPublicID)
		if err != nil {
			return err
		}
		product, err := e.repo.GetProductByID(ctx, payment.ProductID)
		if err != nil {
			return err
		}
		if _, err := e.wallets.Restore(ctx, wallet.SpendRequest{
			Wallet: w, Amount: refund.Amount, CounterAccountCode: ledger.ProductRefundsPayable(product.Code),
			ProductID: payment.ProductID, Kind: dao.JournalReversal, ExternalID: restoreID(refund),
		}, reserve); err != nil {
			return err
		}
	}

	payment.RefundedAmount -= refund.Amount
	if err := e.repo.UpdatePayment(ctx, payment); err != nil {
		return err
	}
	code := "provider_failed"
	refund.Status, refund.FailureCode, refund.FailureReason = dao.RefundFailed, &code, &reason
	if err := e.repo.UpdateRefund(ctx, refund); err != nil {
		return err
	}
	return e.emitRefund(ctx, refund, TopicRefundFailed)
}

// postBetween books a two-legged journal: Dr debitCode, Cr creditCode.
func (e *Engine) postBetween(ctx context.Context, externalID string, kind dao.JournalKind, payment *dao.Payment,
	debitCode, creditCode string, amount int64, currency, memo string, reverses *int64) error {
	debit, err := e.repo.GetLedgerAccountByCode(ctx, debitCode)
	if err != nil {
		return err
	}
	credit, err := e.repo.GetLedgerAccountByCode(ctx, creditCode)
	if err != nil {
		return err
	}
	source := "payment"
	return e.repo.PostJournal(ctx, &dao.Journal{
		PublicID: ids.New(ids.LedgerJournal), ExternalID: externalID, Kind: kind,
		ProductID: &payment.ProductID, SourceKind: &source, SourceID: &payment.PublicID,
		ReversesJournalID: reverses, Memo: memo,
		Postings: []*dao.Posting{
			{AccountID: debit.ID, Direction: dao.Debit, Amount: amount, Currency: currency},
			{AccountID: credit.ID, Direction: dao.Credit, Amount: amount, Currency: currency},
		},
	})
}

type refundEvent struct {
	RefundID      string  `json:"refund_id"`
	PaymentID     string  `json:"payment_id"`
	Amount        int64   `json:"amount"`
	Currency      string  `json:"currency"`
	Status        string  `json:"status"`
	ReasonCode    string  `json:"reason_code"`
	FailureReason *string `json:"failure_reason,omitempty"`
}

func (e *Engine) emitRefund(ctx context.Context, refund *dao.Refund, topic string) error {
	payload, err := json.Marshal(refundEvent{
		RefundID: refund.PublicID, PaymentID: refund.PaymentPublicID, Amount: refund.Amount,
		Currency: refund.Currency, Status: string(refund.Status), ReasonCode: refund.ReasonCode,
		FailureReason: refund.FailureReason,
	})
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to encode refund event")
	}
	return e.repo.SaveOutboxEvent(ctx, &dao.OutboxEvent{
		EventID: ids.New(ids.OutboxEvent), Topic: topic,
		AggregateType: "refund", AggregateID: refund.PublicID, Payload: payload,
	})
}

// processRefundEvent treats a refund webhook as a hint: find the refund,
// fetch its real state, sync to that.
func (e *Engine) processRefundEvent(ctx context.Context, providerName, providerRefundID, reference string) error {
	refund, err := e.repo.GetRefundByProviderRef(ctx, providerName, providerRefundID)
	if apperrors.Is(err, apperrors.NotFound) {
		return apperrors.New(apperrors.Unavailable, "no refund yet for %s refund %s", providerName, providerRefundID)
	}
	if err != nil {
		return err
	}
	p, err := e.provider(providerName)
	if err != nil {
		return err
	}
	pr, err := p.FetchRefund(ctx, providerRefundID)
	if err != nil {
		return err
	}
	_, err = e.SyncRefund(ctx, refund.ID, pr, "webhook")
	return err
}

// PollRefund recovers a refund that has gone quiet: one still initiated is
// submitted again (safe — the refund id is the provider's idempotency key);
// one pending is fetched, for when its webhook never came.
func (e *Engine) PollRefund(ctx context.Context, refund *dao.Refund) (*dao.Refund, error) {
	if refund.Status == dao.RefundInitiated {
		err := e.repo.WithTx(ctx, func(ctx context.Context) error {
			locked, err := e.repo.LockRefund(ctx, refund.ID)
			if err != nil || locked.Status != dao.RefundInitiated {
				return err
			}
			payment, err := e.repo.GetPaymentByID(ctx, locked.PaymentID)
			if err != nil {
				return err
			}
			refund = locked
			return e.submitRefund(ctx, payment, locked)
		})
		return refund, err
	}

	p, err := e.provider(refund.Provider)
	if err != nil {
		return nil, err
	}
	pr, err := p.FetchRefund(ctx, *refund.ProviderRefundID)
	if err != nil {
		return nil, err
	}
	return e.SyncRefund(ctx, refund.ID, pr, "poller")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
