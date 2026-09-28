package service

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/internal/payment"
	"github.com/gofreego/openpay/pkg/apperrors"
)

func (s *Service) CreatePayment(ctx context.Context, req *openpay_v1.CreatePaymentRequest) (*openpay_v1.CreatePaymentResponse, error) {
	productID, err := auth.RequireService(ctx)
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	if req.GetPurpose() != openpay_v1.PaymentPurpose_PAYMENT_PURPOSE_WALLET_TOPUP {
		return nil, apperrors.New(apperrors.InvalidArgument,
			"only WALLET_TOPUP payments are supported until orders exist")
	}

	viewer, err := s.walletViewerFor(ctx, "")
	if err != nil {
		return nil, err
	}

	// idempotent owns the transaction: a retry with the same key returns the
	// same payment and checkout instead of opening a second one.
	return idempotent(ctx, s.repo, "CreatePayment", req,
		func(ctx context.Context) (*openpay_v1.CreatePaymentResponse, error) {
			w, err := s.visibleWallet(ctx, viewer, req.GetWalletId())
			if err != nil {
				return nil, err
			}
			customer, err := s.repo.GetCustomerByPublicID(ctx, w.CustomerPublicID)
			if err != nil {
				return nil, err
			}
			product, err := s.repo.GetProductByID(ctx, productID)
			if err != nil {
				return nil, err
			}

			description := product.Name
			if req.GetDescription() != "" {
				description += " — " + req.GetDescription()
			}
			p, attempt, err := s.payments.CreateTopup(ctx, payment.TopupRequest{
				ProductID: productID, Customer: customer, Wallet: w,
				Amount: req.GetAmount(), Currency: req.GetCurrency(),
				Description: description, ReturnURL: req.GetReturnUrl(),
			})
			if err != nil {
				return nil, err
			}
			if err := s.audit(ctx, auditParams{
				Action: "payment.created", ResourceType: "payment", ResourceID: p.PublicID,
				ProductID: &productID, After: map[string]any{"amount": p.Amount, "wallet_id": w.PublicID, "status": p.Status},
			}); err != nil {
				return nil, err
			}

			out := toProtoPayment(p)
			if attempt != nil && attempt.CheckoutURL != nil && p.Status == dao.PaymentPending {
				out.CheckoutUrl = *attempt.CheckoutURL
			}
			return &openpay_v1.CreatePaymentResponse{Payment: out}, nil
		})
}

// visiblePayment loads a payment the caller may see: a product backend its
// own product's, an operator what their scope grants. Anything else is
// NotFound.
func (s *Service) visiblePayment(ctx context.Context, id, operatorPermission string) (*dao.Payment, bool, error) {
	caller, _ := appcontext.CallerFrom(ctx)
	isOperator := !caller.IsService()

	var scope *filter.ProductScope
	if caller.IsService() {
		scope = filter.OnlyProducts(caller.ProductID)
	} else {
		if err := auth.RequireOperator(ctx, operatorPermission); err != nil {
			return nil, false, err
		}
		var err error
		if scope, err = s.callerScope(ctx); err != nil {
			return nil, false, err
		}
	}

	p, err := s.repo.GetPaymentByPublicID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if err := requireVisible(scope, &p.ProductID, "payment", id); err != nil {
		return nil, false, err
	}
	return p, isOperator, nil
}

func (s *Service) GetPayment(ctx context.Context, req *openpay_v1.GetPaymentRequest) (*openpay_v1.GetPaymentResponse, error) {
	if err := validate(req); err != nil {
		return nil, err
	}
	p, isOperator, err := s.visiblePayment(ctx, req.GetId(), auth.PermPaymentsRead)
	if err != nil {
		return nil, err
	}
	out, err := s.paymentDetail(ctx, p, isOperator)
	if err != nil {
		return nil, err
	}
	return &openpay_v1.GetPaymentResponse{Payment: out}, nil
}

func (s *Service) ListPayments(ctx context.Context, req *openpay_v1.ListPaymentsRequest) (*openpay_v1.ListPaymentsResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermPaymentsRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}

	f := &filter.Payment{
		Limit: int(req.GetLimit()), Offset: int(req.GetOffset()),
		Status: fromProtoPaymentStatus(req.GetStatus()), Scope: scope,
	}
	if req.GetProductId() != "" {
		product, err := s.repo.GetProductByPublicID(ctx, req.GetProductId())
		if err != nil {
			return nil, err
		}
		if err := requireProductInScope(scope, product.ID); err != nil {
			return nil, err
		}
		f.ProductID = &product.ID
	}
	if req.GetCustomerId() != "" {
		customer, err := s.repo.GetCustomerByPublicID(ctx, req.GetCustomerId())
		if err != nil {
			return nil, err
		}
		f.CustomerID = &customer.ID
	}

	payments, total, err := s.repo.ListPayments(ctx, f)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.ListPaymentsResponse{Total: total}
	for _, p := range payments {
		response.Payments = append(response.Payments, toProtoPayment(p))
	}
	return response, nil
}

func (s *Service) SyncPayment(ctx context.Context, req *openpay_v1.SyncPaymentRequest) (*openpay_v1.SyncPaymentResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermPaymentsSync); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	p, _, err := s.visiblePayment(ctx, req.GetId(), auth.PermPaymentsSync)
	if err != nil {
		return nil, err
	}

	// No idempotency key: a sync applies the provider's truth, and applying
	// the same truth twice changes nothing.
	synced, err := s.payments.Poll(ctx, p, "operator")
	if err != nil {
		return nil, err
	}
	out, err := s.paymentDetail(ctx, synced, true)
	if err != nil {
		return nil, err
	}
	return &openpay_v1.SyncPaymentResponse{Payment: out}, nil
}

// paymentDetail adds the checkout to send the customer to, and for operators
// the attempts and transition history — the timeline a support case needs.
func (s *Service) paymentDetail(ctx context.Context, p *dao.Payment, withHistory bool) (*openpay_v1.Payment, error) {
	out := toProtoPayment(p)
	attempts, err := s.repo.ListPaymentAttempts(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if n := len(attempts); n > 0 && p.Status == dao.PaymentPending && attempts[n-1].CheckoutURL != nil {
		out.CheckoutUrl = *attempts[n-1].CheckoutURL
	}
	if !withHistory {
		return out, nil
	}

	for _, a := range attempts {
		out.Attempts = append(out.Attempts, &openpay_v1.PaymentAttempt{
			Id: a.PublicID, Provider: a.Provider, RoutingReason: a.RoutingReason,
			ProviderPaymentId: deref(a.ProviderPaymentID), Status: a.Status,
			FailureCode: deref(a.FailureCode), FailureReason: deref(a.FailureReason),
			CreatedAt: timestamppb.New(a.CreatedAt),
		})
	}
	transitions, err := s.repo.ListPaymentTransitions(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, t := range transitions {
		out.Transitions = append(out.Transitions, &openpay_v1.PaymentTransition{
			From: toProtoPaymentStatus(t.From), To: toProtoPaymentStatus(t.To),
			Source: t.Source, Reference: deref(t.Reference), Detail: deref(t.Detail),
			At: timestamppb.New(t.CreatedAt),
		})
	}
	return out, nil
}

func toProtoPayment(p *dao.Payment) *openpay_v1.Payment {
	out := &openpay_v1.Payment{
		Id: p.PublicID, ProductId: p.ProductPublicID,
		CustomerId: deref(p.CustomerPublicID), WalletId: deref(p.WalletPublicID),
		Purpose: openpay_v1.PaymentPurpose_PAYMENT_PURPOSE_WALLET_TOPUP,
		Amount:  p.Amount, Currency: p.Currency,
		Status:      toProtoPaymentStatus(p.Status),
		Application: toProtoApplication(p.Application),
		Provider:    deref(p.Provider), RefundedAmount: p.RefundedAmount,
		FailureCode: deref(p.FailureCode), FailureReason: deref(p.FailureReason),
		Description: p.Description,
		ExpiresAt:   timestamppb.New(p.ExpiresAt),
		CreatedAt:   timestamppb.New(p.CreatedAt),
		UpdatedAt:   timestamppb.New(p.UpdatedAt),
	}
	if p.Purpose == dao.PurposeOrder {
		out.Purpose = openpay_v1.PaymentPurpose_PAYMENT_PURPOSE_ORDER
	}
	if p.CapturedAmount != nil {
		out.CapturedAmount = *p.CapturedAmount
	}
	return out
}

var paymentStatuses = map[dao.PaymentStatus]openpay_v1.PaymentStatus{
	dao.PaymentCreated:    openpay_v1.PaymentStatus_PAYMENT_STATUS_CREATED,
	dao.PaymentPending:    openpay_v1.PaymentStatus_PAYMENT_STATUS_PENDING,
	dao.PaymentAuthorized: openpay_v1.PaymentStatus_PAYMENT_STATUS_AUTHORIZED,
	dao.PaymentCaptured:   openpay_v1.PaymentStatus_PAYMENT_STATUS_CAPTURED,
	dao.PaymentSettled:    openpay_v1.PaymentStatus_PAYMENT_STATUS_SETTLED,
	dao.PaymentFailed:     openpay_v1.PaymentStatus_PAYMENT_STATUS_FAILED,
	dao.PaymentExpired:    openpay_v1.PaymentStatus_PAYMENT_STATUS_EXPIRED,
	dao.PaymentCancelled:  openpay_v1.PaymentStatus_PAYMENT_STATUS_CANCELLED,

	dao.PaymentPartiallyRefunded: openpay_v1.PaymentStatus_PAYMENT_STATUS_PARTIALLY_REFUNDED,
	dao.PaymentRefunded:          openpay_v1.PaymentStatus_PAYMENT_STATUS_REFUNDED,
	dao.PaymentDisputed:          openpay_v1.PaymentStatus_PAYMENT_STATUS_DISPUTED,
	dao.PaymentDisputeWon:        openpay_v1.PaymentStatus_PAYMENT_STATUS_DISPUTE_WON,
	dao.PaymentDisputeLost:       openpay_v1.PaymentStatus_PAYMENT_STATUS_DISPUTE_LOST,
}

func toProtoPaymentStatus(s dao.PaymentStatus) openpay_v1.PaymentStatus {
	return paymentStatuses[s]
}

func fromProtoPaymentStatus(s openpay_v1.PaymentStatus) dao.PaymentStatus {
	for daoStatus, protoStatus := range paymentStatuses {
		if protoStatus == s {
			return daoStatus
		}
	}
	return ""
}

func toProtoApplication(a dao.PaymentApplication) openpay_v1.PaymentApplication {
	switch a {
	case dao.ApplicationApplied:
		return openpay_v1.PaymentApplication_PAYMENT_APPLICATION_APPLIED
	case dao.ApplicationUnapplied:
		return openpay_v1.PaymentApplication_PAYMENT_APPLICATION_UNAPPLIED
	case dao.ApplicationSuspense:
		return openpay_v1.PaymentApplication_PAYMENT_APPLICATION_SUSPENSE
	default:
		return openpay_v1.PaymentApplication_PAYMENT_APPLICATION_PENDING
	}
}

func (s *Service) CreateRefund(ctx context.Context, req *openpay_v1.CreateRefundRequest) (*openpay_v1.CreateRefundResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermRefundsCreate); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	return idempotent(ctx, s.repo, "CreateRefund", req,
		func(ctx context.Context) (*openpay_v1.CreateRefundResponse, error) {
			p, err := s.repo.GetPaymentByPublicID(ctx, req.GetPaymentId())
			if err != nil {
				return nil, err
			}
			caller, _ := appcontext.CallerFrom(ctx)
			refund, err := s.payments.CreateRefund(ctx, payment.RefundRequest{
				PaymentID: p.ID, Amount: req.GetAmount(), ReasonCode: req.GetReasonCode(),
				Memo: req.GetMemo(), RequestedBy: caller.UserID,
			})
			if err != nil {
				return nil, err
			}
			if err := s.audit(ctx, auditParams{
				Action: "refund.created", ResourceType: "refund", ResourceID: refund.PublicID,
				ProductID: &p.ProductID,
				After: map[string]any{"payment_id": p.PublicID, "amount": refund.Amount,
					"reason_code": refund.ReasonCode, "memo": refund.Memo, "status": refund.Status},
			}); err != nil {
				return nil, err
			}
			return &openpay_v1.CreateRefundResponse{Refund: toProtoRefund(refund)}, nil
		})
}

func (s *Service) GetRefund(ctx context.Context, req *openpay_v1.GetRefundRequest) (*openpay_v1.GetRefundResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermPaymentsRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	refund, err := s.repo.GetRefundByPublicID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if err := requireVisible(scope, &refund.ProductID, "refund", req.GetId()); err != nil {
		return nil, err
	}
	return &openpay_v1.GetRefundResponse{Refund: toProtoRefund(refund)}, nil
}

func (s *Service) ListRefunds(ctx context.Context, req *openpay_v1.ListRefundsRequest) (*openpay_v1.ListRefundsResponse, error) {
	if err := validate(req); err != nil {
		return nil, err
	}
	p, _, err := s.visiblePayment(ctx, req.GetPaymentId(), auth.PermPaymentsRead)
	if err != nil {
		return nil, err
	}
	refunds, err := s.repo.ListPaymentRefunds(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.ListRefundsResponse{}
	for _, r := range refunds {
		response.Refunds = append(response.Refunds, toProtoRefund(r))
	}
	return response, nil
}

var refundStatuses = map[dao.RefundStatus]openpay_v1.RefundStatus{
	dao.RefundInitiated: openpay_v1.RefundStatus_REFUND_STATUS_INITIATED,
	dao.RefundPending:   openpay_v1.RefundStatus_REFUND_STATUS_PENDING,
	dao.RefundProcessed: openpay_v1.RefundStatus_REFUND_STATUS_PROCESSED,
	dao.RefundFailed:    openpay_v1.RefundStatus_REFUND_STATUS_FAILED,
}

func toProtoRefund(r *dao.Refund) *openpay_v1.Refund {
	out := &openpay_v1.Refund{
		Id: r.PublicID, PaymentId: r.PaymentPublicID, Amount: r.Amount, Currency: r.Currency,
		Status: refundStatuses[r.Status], Source: string(r.Source), ReasonCode: r.ReasonCode, Memo: r.Memo,
		ProviderRefundId: deref(r.ProviderRefundID), FailureReason: deref(r.FailureReason),
		RequestedBy: r.RequestedBy, CreatedAt: timestamppb.New(r.CreatedAt),
	}
	if r.ProcessedAt != nil {
		out.ProcessedAt = timestamppb.New(*r.ProcessedAt)
	}
	return out
}

func (s *Service) GetDispute(ctx context.Context, req *openpay_v1.GetDisputeRequest) (*openpay_v1.GetDisputeResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermPaymentsRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	d, err := s.visibleDispute(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return &openpay_v1.GetDisputeResponse{Dispute: toProtoDispute(d)}, nil
}

func (s *Service) visibleDispute(ctx context.Context, id string) (*dao.Dispute, error) {
	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.repo.GetDisputeByPublicID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := requireVisible(scope, &d.ProductID, "dispute", id); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) ListDisputes(ctx context.Context, req *openpay_v1.ListDisputesRequest) (*openpay_v1.ListDisputesResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermPaymentsRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 50
	}
	disputes, err := s.repo.ListDisputes(ctx, scope, fromProtoDisputeStatus(req.GetStatus()), limit)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.ListDisputesResponse{}
	for _, d := range disputes {
		response.Disputes = append(response.Disputes, toProtoDispute(d))
	}
	return response, nil
}

func (s *Service) SubmitDisputeEvidence(ctx context.Context, req *openpay_v1.SubmitDisputeEvidenceRequest) (*openpay_v1.SubmitDisputeEvidenceResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermDisputesManage); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	d, err := s.visibleDispute(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	// No idempotency key: only an open dispute takes evidence, so a retry
	// after success is refused rather than submitted twice.
	caller, _ := appcontext.CallerFrom(ctx)
	var updated *dao.Dispute
	err = s.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		if updated, err = s.payments.SubmitDisputeEvidence(ctx, d.ID, req.GetEvidence(), caller.UserID); err != nil {
			return err
		}
		return s.audit(ctx, auditParams{
			Action: "dispute.evidence_submitted", ResourceType: "dispute", ResourceID: d.PublicID,
			ProductID: &d.ProductID, After: map[string]any{"evidence_bytes": len(req.GetEvidence())},
		})
	})
	if err != nil {
		return nil, err
	}
	return &openpay_v1.SubmitDisputeEvidenceResponse{Dispute: toProtoDispute(updated)}, nil
}

var disputeStatuses = map[dao.DisputeStatus]openpay_v1.DisputeStatus{
	dao.DisputeOpen:        openpay_v1.DisputeStatus_DISPUTE_STATUS_OPEN,
	dao.DisputeUnderReview: openpay_v1.DisputeStatus_DISPUTE_STATUS_UNDER_REVIEW,
	dao.DisputeWon:         openpay_v1.DisputeStatus_DISPUTE_STATUS_WON,
	dao.DisputeLost:        openpay_v1.DisputeStatus_DISPUTE_STATUS_LOST,
}

func fromProtoDisputeStatus(s openpay_v1.DisputeStatus) dao.DisputeStatus {
	for daoStatus, protoStatus := range disputeStatuses {
		if protoStatus == s {
			return daoStatus
		}
	}
	return ""
}

func toProtoDispute(d *dao.Dispute) *openpay_v1.Dispute {
	ts := func(t *time.Time) *timestamppb.Timestamp {
		if t == nil {
			return nil
		}
		return timestamppb.New(*t)
	}
	return &openpay_v1.Dispute{
		Id: d.PublicID, PaymentId: d.PaymentPublicID, Amount: d.Amount, Currency: d.Currency,
		Reason: d.Reason, Status: disputeStatuses[d.Status],
		FromWallet: d.FromWallet, FromUnapplied: d.FromUnapplied, FromExpense: d.FromExpense,
		EvidenceDueBy: ts(d.EvidenceDueBy), Evidence: deref(d.Evidence),
		EvidenceSubmittedBy: deref(d.EvidenceSubmittedBy), EvidenceSubmittedAt: ts(d.EvidenceSubmittedAt),
		ResolvedAt: ts(d.ResolvedAt), CreatedAt: timestamppb.New(d.CreatedAt),
	}
}
