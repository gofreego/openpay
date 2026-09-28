package order

import (
	"context"
	"slices"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/payment"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

const (
	DestinationSource = "source"
	DestinationWallet = "wallet"
)

const TopicOrderRefunded = "order.refunded"

// Breakdown is a refund's own split (plan.md D13).
type Breakdown struct {
	Subtotal, Discount, Tax int64
}

type RefundRequest struct {
	OrderID int64
	Amount  int64
	// Breakdown is the product's split of this refund. nil falls back to
	// splitting proportionally to the order, and the refund says so.
	Breakdown *Breakdown
	// Destination overrides the product's default: "source" or "wallet".
	Destination string
	ReasonCode  string
	Memo        string
	RequestedBy string
}

// Refund returns part or all of a paid order.
//
// The refund carries its own tax split, because a partial refund's tax
// portion cannot be derived legitimately (D13). One journal reverses each
// component against its own account — Dr product_sales, Dr gst_payable,
// Cr discounts — and credits where the money goes:
//
//   - destination "source": each share goes back the way it came. Wallet
//     shares are credited to their wallets; the card share goes into
//     refunds_payable and a provider refund sends it to the card.
//   - destination "wallet": all of it is credited to the customer's
//     refundable wallet as store credit.
//
// The refund is split across the order's tenders real money first — card,
// then purchased wallets, then granted ones — so promotional credit is the
// last thing returned. Every limit is a database constraint: per component
// on the order, and per tender.
func (e *Engine) Refund(ctx context.Context, req RefundRequest) (*dao.OrderRefund, []*dao.OrderRefundPart, error) {
	if req.Amount <= 0 {
		return nil, nil, apperrors.New(apperrors.InvalidArgument, "a refund must be positive")
	}
	if !slices.Contains(payment.RefundReasons, req.ReasonCode) {
		return nil, nil, apperrors.New(apperrors.InvalidArgument, "reason code %q is not one of %v", req.ReasonCode, payment.RefundReasons)
	}
	var (
		refund *dao.OrderRefund
		parts  []*dao.OrderRefundPart
	)
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		o, err := e.repo.LockOrder(ctx, req.OrderID)
		if err != nil {
			return err
		}
		if o.Status != dao.OrderPaid && o.Status != dao.OrderPartiallyRefunded {
			return apperrors.New(apperrors.FailedPrecondition, "order %s is %s and has nothing to refund", o.PublicID, o.Status)
		}
		product, err := e.repo.GetProductByID(ctx, o.ProductID)
		if err != nil {
			return err
		}

		split, provided, err := splitRefund(o, req)
		if err != nil {
			return err
		}
		destination := req.Destination
		if destination == "" {
			destination = product.RefundDestination
		}
		if destination != DestinationSource && destination != DestinationWallet {
			return apperrors.New(apperrors.InvalidArgument, "destination must be source or wallet")
		}

		refund = &dao.OrderRefund{
			PublicID: ids.New(ids.Refund), OrderID: o.ID, ProductID: o.ProductID, Amount: req.Amount,
			Subtotal: split.Subtotal, Discount: split.Discount, Tax: split.Tax, TaxBreakdownProvided: provided,
			Destination: destination, ReasonCode: req.ReasonCode, Memo: req.Memo, RequestedBy: req.RequestedBy,
			OrderPublicID: o.PublicID,
		}
		if err := e.repo.CreateOrderRefund(ctx, refund); err != nil {
			return err
		}

		o.RefundedSubtotal += split.Subtotal
		o.RefundedDiscount += split.Discount
		o.RefundedTax += split.Tax
		o.Status = dao.OrderPartiallyRefunded
		if o.RefundedSubtotal-o.RefundedDiscount+o.RefundedTax == o.Total {
			o.Status = dao.OrderRefunded
		}
		if err := e.repo.UpdateOrder(ctx, o); err != nil {
			return err
		}

		parts, err = e.refundTenders(ctx, o, product, refund, split)
		if err != nil {
			return err
		}
		return e.emit(ctx, o, TopicOrderRefunded)
	})
	return refund, parts, err
}

// splitRefund returns the refund's breakdown: the product's, checked, or a
// proportional one when the product sent none — the documented fallback,
// and flagged so finance can see which refunds were split for them.
func splitRefund(o *dao.Order, req RefundRequest) (Breakdown, bool, error) {
	if b := req.Breakdown; b != nil {
		if b.Subtotal < 0 || b.Discount < 0 || b.Tax < 0 || b.Subtotal-b.Discount+b.Tax != req.Amount {
			return Breakdown{}, false, apperrors.New(apperrors.InvalidArgument,
				"refund subtotal %d - discount %d + tax %d must equal its amount %d", b.Subtotal, b.Discount, b.Tax, req.Amount)
		}
		return *b, true, nil
	}
	if !o.TaxBreakdownProvided {
		// The order was booked gross; so is its refund.
		return Breakdown{Subtotal: req.Amount}, false, nil
	}
	tax := req.Amount * o.Tax / o.Total
	discount := req.Amount * o.Discount / o.Total
	return Breakdown{Subtotal: req.Amount + discount - tax, Discount: discount, Tax: tax}, false, nil
}

// refundTenders splits the refund across the tenders that paid the order,
// posts the reversing journal, and starts any card refund.
func (e *Engine) refundTenders(ctx context.Context, o *dao.Order, product *dao.Product,
	refund *dao.OrderRefund, split Breakdown) ([]*dao.OrderRefundPart, error) {
	tenders, err := e.repo.ListOrderTenders(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	ordered, err := e.refundOrder(ctx, tenders)
	if err != nil {
		return nil, err
	}

	var storeCredit *dao.Wallet
	if refund.Destination == DestinationWallet {
		if storeCredit, err = e.refundWallet(ctx, o); err != nil {
			return nil, err
		}
	}

	payable, err := e.repo.GetLedgerAccountByCode(ctx, ledger.ProductRefundsPayable(product.Code))
	if err != nil {
		return nil, err
	}
	var (
		parts      []*dao.OrderRefundPart
		credits    []*dao.Posting
		wallets    []*dao.Wallet
		cardShare  int64
		cardTender *dao.OrderTender
		remaining  = refund.Amount
	)
	for _, t := range ordered {
		take := min(remaining, t.Amount-t.RefundedAmount)
		if take <= 0 {
			continue
		}
		remaining -= take
		t.RefundedAmount += take
		if err := e.repo.UpdateOrderTender(ctx, t); err != nil {
			return nil, err
		}

		part := &dao.OrderRefundPart{OrderRefundID: refund.ID, TenderID: t.ID, Amount: take}
		switch {
		case storeCredit != nil:
			part.WalletID, part.WalletPublicID = &storeCredit.ID, &storeCredit.PublicID
		case t.Kind == dao.TenderWallet:
			w, err := e.repo.GetWalletByPublicID(ctx, *t.WalletPublicID)
			if err != nil {
				return nil, err
			}
			if err := e.wallets.CheckRefundIn(ctx, w, take); err != nil {
				return nil, err
			}
			part.WalletID, part.WalletPublicID = &w.ID, &w.PublicID
			credits = append(credits, &dao.Posting{AccountID: w.LedgerAccountID, Direction: dao.Credit, Amount: take, Currency: o.Currency})
			wallets = append(wallets, w)
		default:
			cardShare += take
			cardTender = t
		}
		parts = append(parts, part)
		if remaining == 0 {
			break
		}
	}
	if remaining != 0 {
		return nil, apperrors.New(apperrors.FailedPrecondition, "order %s has only %d left to refund", o.PublicID, refund.Amount-remaining)
	}
	if storeCredit != nil {
		if err := e.wallets.CheckRefundIn(ctx, storeCredit, refund.Amount); err != nil {
			return nil, err
		}
		credits = append(credits, &dao.Posting{AccountID: storeCredit.LedgerAccountID, Direction: dao.Credit, Amount: refund.Amount, Currency: o.Currency})
		wallets = append(wallets, storeCredit)
	}
	if cardShare > 0 {
		credits = append(credits, &dao.Posting{AccountID: payable.ID, Direction: dao.Credit, Amount: cardShare, Currency: o.Currency})
	}

	journal, err := e.refundJournal(ctx, o, product, refund, split, credits)
	if err != nil {
		return nil, err
	}
	if err := e.repo.PostJournal(ctx, journal); err != nil {
		return nil, err
	}
	if err := e.wallets.EmitMovements(ctx, journal, wallets); err != nil {
		return nil, err
	}

	if cardShare > 0 {
		p, err := e.repo.GetOrderPayment(ctx, o.ID)
		if err != nil {
			return nil, err
		}
		cardRefund, err := e.payments.CreateRefund(ctx, payment.RefundRequest{
			PaymentID: p.ID, Amount: cardShare, ReasonCode: refund.ReasonCode,
			Memo: "order refund " + refund.PublicID, RequestedBy: refund.RequestedBy,
		})
		if err != nil {
			return nil, err
		}
		for _, part := range parts {
			if part.TenderID == cardTender.ID {
				part.RefundID, part.RefundPublicID = &cardRefund.ID, &cardRefund.PublicID
			}
		}
	}
	for _, part := range parts {
		if err := e.repo.CreateOrderRefundPart(ctx, part); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

// refundOrder puts real money first: the card, then purchased wallets, then
// granted ones — the reverse of the order they were spent in.
func (e *Engine) refundOrder(ctx context.Context, tenders []*dao.OrderTender) ([]*dao.OrderTender, error) {
	rank := map[int64]int{}
	for _, t := range tenders {
		if t.Kind == dao.TenderGateway {
			rank[t.ID] = 0
			continue
		}
		w, err := e.repo.GetWalletByPublicID(ctx, *t.WalletPublicID)
		if err != nil {
			return nil, err
		}
		walletType, err := e.repo.GetWalletTypeByID(ctx, w.WalletTypeID)
		if err != nil {
			return nil, err
		}
		rank[t.ID] = 1
		if !walletType.Fundable {
			rank[t.ID] = 2
		}
	}
	ordered := slices.Clone(tenders)
	slices.SortStableFunc(ordered, func(a, b *dao.OrderTender) int { return rank[a.ID] - rank[b.ID] })
	return ordered, nil
}

// refundWallet is where store credit goes: the customer's wallet of the
// product's first fundable, refundable wallet type by code — MAIN, for the
// default types — opened if they have none yet. Store credit is real money
// (the customer paid for what is being refunded), so a granted-only type
// never qualifies.
func (e *Engine) refundWallet(ctx context.Context, o *dao.Order) (*dao.Wallet, error) {
	if o.CustomerPublicID == nil {
		return nil, apperrors.New(apperrors.FailedPrecondition, "a guest order has no wallet to refund to; refund it to source")
	}
	types, err := e.repo.ListWalletTypes(ctx, o.ProductID)
	if err != nil {
		return nil, err
	}
	for _, wt := range types {
		if wt.ProductID == nil || !wt.Fundable || !wt.RefundableToSource || wt.Status != dao.WalletTypeActive {
			continue
		}
		customer, err := e.repo.GetCustomerByPublicID(ctx, *o.CustomerPublicID)
		if err != nil {
			return nil, err
		}
		w, _, err := e.wallets.Open(ctx, customer, wt)
		return w, err
	}
	return nil, apperrors.New(apperrors.FailedPrecondition, "the product has no fundable, refundable wallet type to credit")
}

// refundJournal reverses the order's components by the refund's own split:
//
//	Dr income:<product>:product_sales   subtotal
//	Dr liability:gst_payable            tax
//	Cr expense:<product>:discounts      discount
//	Cr each destination                 (wallets, refunds_payable)
func (e *Engine) refundJournal(ctx context.Context, o *dao.Order, product *dao.Product, refund *dao.OrderRefund,
	split Breakdown, credits []*dao.Posting) (*dao.Journal, error) {
	postings := slices.Clone(credits)
	for _, leg := range []struct {
		code      string
		direction dao.Direction
		amount    int64
	}{
		{ledger.ProductSales(product.Code), dao.Debit, split.Subtotal},
		{ledger.GSTPayable, dao.Debit, split.Tax},
		{ledger.ProductDiscounts(product.Code), dao.Credit, split.Discount},
	} {
		if leg.amount == 0 {
			continue
		}
		a, err := e.repo.GetLedgerAccountByCode(ctx, leg.code)
		if err != nil {
			return nil, err
		}
		postings = append(postings, &dao.Posting{AccountID: a.ID, Direction: leg.direction, Amount: leg.amount, Currency: o.Currency})
	}
	settlement, err := e.repo.GetJournalByExternalID(ctx, "order:"+o.PublicID+":paid")
	if err != nil {
		return nil, err
	}
	source := "order"
	memo := "refund of order " + o.ExternalRef
	if !refund.TaxBreakdownProvided {
		memo += " (tax split allocated proportionally: no breakdown provided)"
	}
	return &dao.Journal{
		PublicID: ids.New(ids.LedgerJournal), ExternalID: "order:" + o.PublicID + ":refund:" + refund.PublicID,
		Kind: dao.JournalRefund, ProductID: &o.ProductID, SourceKind: &source, SourceID: &o.PublicID,
		ReversesJournalID: &settlement.ID, ReasonCode: &refund.ReasonCode, Memo: memo, Postings: postings,
	}, nil
}
