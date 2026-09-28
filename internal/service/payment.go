package service

import (
	"context"

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
		Provider:    deref(p.Provider),
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
