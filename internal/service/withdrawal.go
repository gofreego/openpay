package service

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/internal/withdrawal"
	"github.com/gofreego/openpay/pkg/apperrors"
)

func (s *Service) AddBeneficiary(ctx context.Context, req *openpay_v1.AddBeneficiaryRequest) (*openpay_v1.AddBeneficiaryResponse, error) {
	if _, err := auth.RequireService(ctx); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	customer, err := s.repo.GetCustomerByPublicID(ctx, req.GetCustomerId())
	if err != nil {
		return nil, err
	}
	b := &dao.Beneficiary{CustomerID: customer.ID, Kind: req.GetKind(), Name: req.GetName()}
	switch req.GetKind() {
	case "bank_account":
		if req.GetAccountNumber() == "" || req.GetIfsc() == "" || req.GetVpa() != "" {
			return nil, apperrors.New(apperrors.InvalidArgument, "a bank account needs account_number and ifsc, and no vpa")
		}
		account, ifsc := req.GetAccountNumber(), req.GetIfsc()
		b.AccountNumber, b.IFSC = &account, &ifsc
	case "vpa":
		if req.GetVpa() == "" || req.GetAccountNumber() != "" || req.GetIfsc() != "" {
			return nil, apperrors.New(apperrors.InvalidArgument, "a vpa beneficiary needs vpa, and no account details")
		}
		vpa := req.GetVpa()
		b.VPA = &vpa
	}
	if _, err := s.withdrawals.AddBeneficiary(ctx, b); err != nil {
		return nil, err
	}
	return &openpay_v1.AddBeneficiaryResponse{Beneficiary: toProtoBeneficiary(b, customer.PublicID)}, nil
}

func (s *Service) ListBeneficiaries(ctx context.Context, req *openpay_v1.ListBeneficiariesRequest) (*openpay_v1.ListBeneficiariesResponse, error) {
	caller, _ := appcontext.CallerFrom(ctx)
	if !caller.IsService() {
		if err := auth.RequireOperator(ctx, auth.PermWithdrawalsRead); err != nil {
			return nil, err
		}
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	customer, err := s.repo.GetCustomerByPublicID(ctx, req.GetCustomerId())
	if err != nil {
		return nil, err
	}
	list, err := s.repo.ListBeneficiaries(ctx, customer.ID)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.ListBeneficiariesResponse{}
	for _, b := range list {
		response.Beneficiaries = append(response.Beneficiaries, toProtoBeneficiary(b, customer.PublicID))
	}
	return response, nil
}

func (s *Service) RequestWithdrawal(ctx context.Context, req *openpay_v1.RequestWithdrawalRequest) (*openpay_v1.RequestWithdrawalResponse, error) {
	if _, err := auth.RequireService(ctx); err != nil {
		return nil, err
	}
	viewer, err := s.walletViewerFor(ctx, "")
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	caller, _ := appcontext.CallerFrom(ctx)

	return idempotent(ctx, s.repo, "RequestWithdrawal", req,
		func(ctx context.Context) (*openpay_v1.RequestWithdrawalResponse, error) {
			w, err := s.visibleWallet(ctx, viewer, req.GetWalletId())
			if err != nil {
				return nil, err
			}
			b, err := s.repo.GetBeneficiaryByPublicID(ctx, req.GetBeneficiaryId())
			if err != nil {
				return nil, err
			}
			x, err := s.withdrawals.Request(ctx, withdrawal.Request{Wallet: w, Beneficiary: b, Amount: req.GetAmount(),
				RequestedBy: caller.CredentialID})
			if err != nil {
				return nil, err
			}
			if err := s.audit(ctx, auditParams{
				Action: "withdrawal.requested", ResourceType: "withdrawal", ResourceID: x.PublicID, ProductID: x.ProductID,
				After: map[string]any{"amount": x.Amount, "wallet_id": w.PublicID, "requires_approval": x.RequiresApproval},
			}); err != nil {
				return nil, err
			}
			return &openpay_v1.RequestWithdrawalResponse{Withdrawal: toProtoWithdrawal(x)}, nil
		})
}

func (s *Service) ApproveWithdrawal(ctx context.Context, req *openpay_v1.DecideWithdrawalRequest) (*openpay_v1.DecideWithdrawalResponse, error) {
	return s.decideWithdrawal(ctx, req, s.withdrawals.Approve, "withdrawal.approved")
}

func (s *Service) RejectWithdrawal(ctx context.Context, req *openpay_v1.DecideWithdrawalRequest) (*openpay_v1.DecideWithdrawalResponse, error) {
	return s.decideWithdrawal(ctx, req, s.withdrawals.Reject, "withdrawal.rejected")
}

// decideWithdrawal takes no idempotency key: a withdrawal is decided once,
// and a retry after success is refused rather than applied twice.
func (s *Service) decideWithdrawal(ctx context.Context, req *openpay_v1.DecideWithdrawalRequest,
	decide func(ctx context.Context, id int64, approver, note string) (*dao.Withdrawal, error), action string) (*openpay_v1.DecideWithdrawalResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermWithdrawalsApprove); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	caller, _ := appcontext.CallerFrom(ctx)
	existing, err := s.repo.GetWithdrawalByPublicID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	var x *dao.Withdrawal
	err = s.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		if x, err = decide(ctx, existing.ID, caller.UserID, req.GetNote()); err != nil {
			return err
		}
		return s.audit(ctx, auditParams{
			Action: action, ResourceType: "withdrawal", ResourceID: x.PublicID, ProductID: x.ProductID,
			After: map[string]any{"amount": x.Amount, "note": req.GetNote(), "status": x.Status},
		})
	})
	if err != nil {
		return nil, err
	}
	return &openpay_v1.DecideWithdrawalResponse{Withdrawal: toProtoWithdrawal(x)}, nil
}

func (s *Service) GetWithdrawal(ctx context.Context, req *openpay_v1.GetWithdrawalRequest) (*openpay_v1.GetWithdrawalResponse, error) {
	if err := validate(req); err != nil {
		return nil, err
	}
	scope, err := s.withdrawalScope(ctx)
	if err != nil {
		return nil, err
	}
	x, err := s.repo.GetWithdrawalByPublicID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if err := requireVisible(scope, x.ProductID, "withdrawal", req.GetId()); err != nil {
		return nil, err
	}
	return &openpay_v1.GetWithdrawalResponse{Withdrawal: toProtoWithdrawal(x)}, nil
}

func (s *Service) ListWithdrawals(ctx context.Context, req *openpay_v1.ListWithdrawalsRequest) (*openpay_v1.ListWithdrawalsResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermWithdrawalsRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	var customerID *int64
	if req.GetCustomerId() != "" {
		c, err := s.repo.GetCustomerByPublicID(ctx, req.GetCustomerId())
		if err != nil {
			return nil, err
		}
		customerID = &c.ID
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 50
	}
	list, err := s.repo.ListWithdrawals(ctx, scope, fromProtoWithdrawalStatus(req.GetStatus()), customerID, limit)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.ListWithdrawalsResponse{}
	for _, x := range list {
		response.Withdrawals = append(response.Withdrawals, toProtoWithdrawal(x))
	}
	return response, nil
}

// withdrawalScope: a product backend sees its own product's withdrawals, an
// operator what their scope grants.
func (s *Service) withdrawalScope(ctx context.Context) (*filter.ProductScope, error) {
	caller, _ := appcontext.CallerFrom(ctx)
	if caller.IsService() {
		return filter.OnlyProducts(caller.ProductID), nil
	}
	if err := auth.RequireOperator(ctx, auth.PermWithdrawalsRead); err != nil {
		return nil, err
	}
	return s.callerScope(ctx)
}

var withdrawalStatuses = map[dao.WithdrawalStatus]openpay_v1.WithdrawalStatus{
	dao.WithdrawalPendingApproval: openpay_v1.WithdrawalStatus_WITHDRAWAL_STATUS_PENDING_APPROVAL,
	dao.WithdrawalApproved:        openpay_v1.WithdrawalStatus_WITHDRAWAL_STATUS_APPROVED,
	dao.WithdrawalProcessing:      openpay_v1.WithdrawalStatus_WITHDRAWAL_STATUS_PROCESSING,
	dao.WithdrawalPaid:            openpay_v1.WithdrawalStatus_WITHDRAWAL_STATUS_PAID,
	dao.WithdrawalFailed:          openpay_v1.WithdrawalStatus_WITHDRAWAL_STATUS_FAILED,
	dao.WithdrawalRejected:        openpay_v1.WithdrawalStatus_WITHDRAWAL_STATUS_REJECTED,
	dao.WithdrawalReversed:        openpay_v1.WithdrawalStatus_WITHDRAWAL_STATUS_REVERSED,
}

func fromProtoWithdrawalStatus(s openpay_v1.WithdrawalStatus) dao.WithdrawalStatus {
	for daoStatus, protoStatus := range withdrawalStatuses {
		if protoStatus == s {
			return daoStatus
		}
	}
	return ""
}

func toProtoWithdrawal(x *dao.Withdrawal) *openpay_v1.Withdrawal {
	out := &openpay_v1.Withdrawal{
		Id: x.PublicID, WalletId: x.WalletPublicID, BeneficiaryId: x.BeneficiaryPublicID, Amount: x.Amount,
		Currency: x.Currency, Status: withdrawalStatuses[x.Status], RequiresApproval: x.RequiresApproval,
		RequestedBy: x.RequestedBy, DecidedBy: deref(x.DecidedBy), DecisionNote: deref(x.DecisionNote),
		FailureReason: deref(x.FailureReason), CreatedAt: timestamppb.New(x.CreatedAt),
	}
	if x.DecidedAt != nil {
		out.DecidedAt = timestamppb.New(*x.DecidedAt)
	}
	if x.PaidAt != nil {
		out.PaidAt = timestamppb.New(*x.PaidAt)
	}
	return out
}

func toProtoBeneficiary(b *dao.Beneficiary, customerID string) *openpay_v1.Beneficiary {
	out := &openpay_v1.Beneficiary{
		Id: b.PublicID, CustomerId: customerID, Kind: b.Kind, Name: b.Name, Ifsc: deref(b.IFSC), Vpa: deref(b.VPA),
		Status: string(b.Status), NameAtBank: deref(b.NameAtBank), FailureReason: deref(b.FailureReason),
		CreatedAt: timestamppb.New(b.CreatedAt),
	}
	if b.AccountNumber != nil {
		n := *b.AccountNumber
		out.AccountNumberMasked = "XXXX" + n[max(len(n)-4, 0):]
	}
	if b.VerifiedAt != nil {
		out.VerifiedAt = timestamppb.New(*b.VerifiedAt)
	}
	return out
}
