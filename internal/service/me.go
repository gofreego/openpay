package service

import (
	"context"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/constants"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// GetMe reports the calling operator's permissions and product scope, as
// resolved for every other request (callerScope), so the console and the
// server cannot disagree about what an operator may reach.
func (s *Service) GetMe(ctx context.Context, _ *openpay_v1.GetMeRequest) (*openpay_v1.GetMeResponse, error) {
	caller, ok := appcontext.CallerFrom(ctx)
	if !ok || !caller.IsOperator() {
		return nil, apperrors.New(apperrors.PermissionDenied, "only an operator has a console identity")
	}
	all, _ := auth.OperatorScope(caller)
	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}

	// Products are few — a page of the largest size holds them all, and is
	// simpler than a query per grant.
	f := &filter.Product{Scope: scope, Limit: constants.MaxPageSize}
	f.WithDefaults()
	products, _, err := s.repo.ListProducts(ctx, f)
	if err != nil {
		return nil, err
	}
	out := &openpay_v1.GetMeResponse{UserId: caller.UserID, Permissions: caller.Permissions, ScopeAll: all}
	for _, p := range products {
		out.Products = append(out.Products, toProtoProduct(p))
	}
	return out, nil
}
