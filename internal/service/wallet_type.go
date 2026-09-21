package service

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

func (s *Service) CreateWalletType(ctx context.Context, req *openpay_v1.CreateWalletTypeRequest) (*openpay_v1.CreateWalletTypeResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermWalletTypesWrite); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	if err := checkWalletCapabilities(req); err != nil {
		return nil, err
	}
	// Enabling withdrawal is a separate decision from configuring a wallet, so
	// it takes a separate permission (plan.md D10).
	if req.GetCapabilities().GetWithdrawable() {
		if err := auth.RequireOperator(ctx, auth.PermWalletTypesApproveWithdrawal); err != nil {
			return nil, err
		}
	}

	return idempotent(ctx, s.repo, "CreateWalletType", req,
		func(ctx context.Context) (*openpay_v1.CreateWalletTypeResponse, error) {
			walletType, err := s.buildWalletType(ctx, req)
			if err != nil {
				return nil, err
			}
			if err := s.repo.CreateWalletType(ctx, walletType); err != nil {
				return nil, err
			}
			if err := s.audit(ctx, auditParams{
				Action:       "wallet_type.created",
				ResourceType: "wallet_type",
				ResourceID:   walletType.PublicID,
				ProductID:    walletType.ProductID,
				After:        walletType,
			}); err != nil {
				return nil, err
			}
			return &openpay_v1.CreateWalletTypeResponse{WalletType: toProtoWalletType(walletType)}, nil
		})
}

// buildWalletType resolves the product and assembles the row.
func (s *Service) buildWalletType(ctx context.Context, req *openpay_v1.CreateWalletTypeRequest) (*dao.WalletType, error) {
	caps := req.GetCapabilities()

	walletType := &dao.WalletType{
		PublicID:           ids.New(ids.WalletType),
		Scope:              dao.WalletScopePlatform,
		Code:               req.GetCode(),
		Name:               req.GetName(),
		Currency:           req.GetCurrency(),
		Fundable:           caps.GetFundable(),
		Grantable:          caps.GetGrantable(),
		Withdrawable:       caps.GetWithdrawable(),
		Transferable:       caps.GetTransferable(),
		RefundableToSource: caps.GetRefundableToSource(),
		AllowNegative:      caps.GetAllowNegative(),
		ExpiryPolicy:       fromProtoExpiryPolicy(req.GetExpiryPolicy()),
		Status:             dao.WalletTypeActive,
	}

	if req.GetProductId() != "" {
		product, err := s.repo.GetProductByPublicID(ctx, req.GetProductId())
		if err != nil {
			return nil, err
		}
		walletType.Scope = dao.WalletScopeProduct
		walletType.ProductID = &product.ID
		// Set here rather than re-reading after the insert: the create path
		// never goes back through the join that populates it.
		walletType.ProductPublicID = &product.PublicID
	}

	if walletType.ExpiryPolicy != dao.ExpiryNone {
		days := int(req.GetExpiryDays())
		walletType.ExpiryDays = &days
	}

	if limits := req.GetLimits(); limits != nil {
		walletType.MaxBalance = optionalLimit(limits.GetMaxBalance())
		walletType.MaxTxnAmount = optionalLimit(limits.GetMaxTxnAmount())
		walletType.DailyLoadLimit = optionalLimit(limits.GetDailyLoadLimit())
	}

	if walletType.Withdrawable {
		caller, _ := appcontext.CallerFrom(ctx)
		approvedAt := time.Now()
		ref := req.GetWithdrawalApprovalRef()
		walletType.WithdrawableApprovedBy = &caller.UserID
		walletType.WithdrawableApprovedAt = &approvedAt
		walletType.WithdrawableApprovalRef = &ref
	}

	return walletType, nil
}

// checkWalletCapabilities enforces D10's coherence rules.
//
// The database has the same rules as CHECK constraints, and they are the real
// guarantee. These run first only to say *why* a combination was refused —
// a constraint violation tells a caller nothing actionable.
func checkWalletCapabilities(req *openpay_v1.CreateWalletTypeRequest) error {
	caps := req.GetCapabilities()

	if !caps.GetFundable() && !caps.GetGrantable() {
		return apperrors.New(apperrors.InvalidArgument,
			"a wallet type must be fundable or grantable, otherwise its balance can never be anything but zero")
	}

	if caps.GetWithdrawable() && !caps.GetFundable() {
		return apperrors.New(apperrors.InvalidArgument,
			"a withdrawable wallet type must also be fundable: allowing cash-out of money nobody paid in is a leak, not a feature")
	}

	// Promotional balance convertible to cash is a fraud target: grant
	// yourself credit, withdraw real money.
	if caps.GetWithdrawable() && caps.GetGrantable() {
		return apperrors.New(apperrors.InvalidArgument,
			"a wallet type cannot be both grantable and withdrawable: granted balance that can be cashed out turns promotions into a cash-out channel")
	}

	if caps.GetWithdrawable() && req.GetWithdrawalApprovalRef() == "" {
		return apperrors.New(apperrors.InvalidArgument,
			"withdrawal_approval_ref is required to enable withdrawable: it records the compliance sign-off this configuration rests on")
	}

	policy := req.GetExpiryPolicy()
	if policy == openpay_v1.ExpiryPolicy_EXPIRY_POLICY_NONE && req.GetExpiryDays() != 0 {
		return apperrors.New(apperrors.InvalidArgument,
			"expiry_days must be zero when expiry_policy is NONE")
	}
	if policy != openpay_v1.ExpiryPolicy_EXPIRY_POLICY_NONE && req.GetExpiryDays() <= 0 {
		return apperrors.New(apperrors.InvalidArgument,
			"expiry_days must be positive when an expiry policy is set")
	}

	return nil
}

func (s *Service) GetWalletType(ctx context.Context, req *openpay_v1.GetWalletTypeRequest) (*openpay_v1.GetWalletTypeResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermWalletTypesRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	walletType, err := s.repo.GetWalletTypeByPublicID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return &openpay_v1.GetWalletTypeResponse{WalletType: toProtoWalletType(walletType)}, nil
}

func (s *Service) ListWalletTypes(ctx context.Context, req *openpay_v1.ListWalletTypesRequest) (*openpay_v1.ListWalletTypesResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermWalletTypesRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	product, err := s.repo.GetProductByPublicID(ctx, req.GetProductId())
	if err != nil {
		return nil, err
	}

	walletTypes, err := s.repo.ListWalletTypes(ctx, product.ID)
	if err != nil {
		return nil, err
	}

	response := &openpay_v1.ListWalletTypesResponse{}
	for _, walletType := range walletTypes {
		response.WalletTypes = append(response.WalletTypes, toProtoWalletType(walletType))
	}
	return response, nil
}

func (s *Service) UpdateWalletType(ctx context.Context, req *openpay_v1.UpdateWalletTypeRequest) (*openpay_v1.UpdateWalletTypeResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermWalletTypesWrite); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	return idempotent(ctx, s.repo, "UpdateWalletType", req,
		func(ctx context.Context) (*openpay_v1.UpdateWalletTypeResponse, error) {
			before, err := s.repo.GetWalletTypeByPublicID(ctx, req.GetId())
			if err != nil {
				return nil, err
			}

			updated := &dao.WalletType{
				PublicID: req.GetId(),
				Name:     req.GetName(),
				Status:   fromProtoWalletTypeStatus(req.GetStatus()),
			}
			if limits := req.GetLimits(); limits != nil {
				updated.MaxBalance = optionalLimit(limits.GetMaxBalance())
				updated.MaxTxnAmount = optionalLimit(limits.GetMaxTxnAmount())
				updated.DailyLoadLimit = optionalLimit(limits.GetDailyLoadLimit())
			}

			if err := s.repo.UpdateWalletType(ctx, updated); err != nil {
				return nil, err
			}
			if err := s.audit(ctx, auditParams{
				Action:       "wallet_type.updated",
				ResourceType: "wallet_type",
				ResourceID:   updated.PublicID,
				ProductID:    updated.ProductID,
				Before:       before,
				After:        updated,
			}); err != nil {
				return nil, err
			}
			return &openpay_v1.UpdateWalletTypeResponse{WalletType: toProtoWalletType(updated)}, nil
		})
}

// optionalLimit turns the proto's zero-means-unlimited into a nullable column.
func optionalLimit(v int64) *int64 {
	if v <= 0 {
		return nil
	}
	return &v
}

func toProtoWalletType(w *dao.WalletType) *openpay_v1.WalletType {
	out := &openpay_v1.WalletType{
		Id:       w.PublicID,
		Scope:    toProtoWalletScope(w.Scope),
		Currency: w.Currency,
		Code:     w.Code,
		Name:     w.Name,
		Capabilities: &openpay_v1.WalletCapabilities{
			Fundable:           w.Fundable,
			Grantable:          w.Grantable,
			Withdrawable:       w.Withdrawable,
			Transferable:       w.Transferable,
			RefundableToSource: w.RefundableToSource,
			AllowNegative:      w.AllowNegative,
		},
		ExpiryPolicy: toProtoExpiryPolicy(w.ExpiryPolicy),
		Limits: &openpay_v1.WalletLimits{
			MaxBalance:     derefLimit(w.MaxBalance),
			MaxTxnAmount:   derefLimit(w.MaxTxnAmount),
			DailyLoadLimit: derefLimit(w.DailyLoadLimit),
		},
		Status:    toProtoWalletTypeStatus(w.Status),
		CreatedAt: timestamppb.New(w.CreatedAt),
		UpdatedAt: timestamppb.New(w.UpdatedAt),
	}
	if w.ProductPublicID != nil {
		out.ProductId = *w.ProductPublicID
	}
	if w.ExpiryDays != nil {
		out.ExpiryDays = int32(*w.ExpiryDays)
	}
	if w.WithdrawableApprovedBy != nil {
		out.WithdrawalApproval = &openpay_v1.WithdrawalApproval{
			ApprovedBy: *w.WithdrawableApprovedBy,
		}
		if w.WithdrawableApprovedAt != nil {
			out.WithdrawalApproval.ApprovedAt = timestamppb.New(*w.WithdrawableApprovedAt)
		}
		if w.WithdrawableApprovalRef != nil {
			out.WithdrawalApproval.Reference = *w.WithdrawableApprovalRef
		}
	}
	return out
}

func derefLimit(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func toProtoWalletScope(s dao.WalletScope) openpay_v1.WalletScope {
	if s == dao.WalletScopePlatform {
		return openpay_v1.WalletScope_WALLET_SCOPE_PLATFORM
	}
	return openpay_v1.WalletScope_WALLET_SCOPE_PRODUCT
}

func toProtoExpiryPolicy(p dao.ExpiryPolicy) openpay_v1.ExpiryPolicy {
	switch p {
	case dao.ExpiryFixed:
		return openpay_v1.ExpiryPolicy_EXPIRY_POLICY_FIXED
	case dao.ExpiryRolling:
		return openpay_v1.ExpiryPolicy_EXPIRY_POLICY_ROLLING
	default:
		return openpay_v1.ExpiryPolicy_EXPIRY_POLICY_NONE
	}
}

func fromProtoExpiryPolicy(p openpay_v1.ExpiryPolicy) dao.ExpiryPolicy {
	switch p {
	case openpay_v1.ExpiryPolicy_EXPIRY_POLICY_FIXED:
		return dao.ExpiryFixed
	case openpay_v1.ExpiryPolicy_EXPIRY_POLICY_ROLLING:
		return dao.ExpiryRolling
	default:
		return dao.ExpiryNone
	}
}

func toProtoWalletTypeStatus(s dao.WalletTypeStatus) openpay_v1.WalletTypeStatus {
	if s == dao.WalletTypeArchived {
		return openpay_v1.WalletTypeStatus_WALLET_TYPE_STATUS_ARCHIVED
	}
	return openpay_v1.WalletTypeStatus_WALLET_TYPE_STATUS_ACTIVE
}

func fromProtoWalletTypeStatus(s openpay_v1.WalletTypeStatus) dao.WalletTypeStatus {
	if s == openpay_v1.WalletTypeStatus_WALLET_TYPE_STATUS_ARCHIVED {
		return dao.WalletTypeArchived
	}
	return dao.WalletTypeActive
}
