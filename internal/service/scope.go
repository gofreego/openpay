package service

import (
	"context"

	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// callerScope resolves which products the caller may see (plan.md D8, U-D6).
//
// Every product-scoped read goes through this, and both kinds of caller end
// at the same answer: a product backend sees its own product, an operator sees
// what their scope permissions grant, and anyone else sees nothing.
func (s *Service) callerScope(ctx context.Context) (*filter.ProductScope, error) {
	caller, ok := appcontext.CallerFrom(ctx)
	if !ok {
		return filter.OnlyProducts(), nil
	}

	switch caller.Kind {
	case appcontext.KindService:
		return filter.OnlyProducts(caller.ProductID), nil
	case appcontext.KindOperator:
		all, codes := auth.OperatorScope(caller)
		if all {
			return filter.AllProducts(), nil
		}
		ids := make([]int64, 0, len(codes))
		for _, code := range codes {
			product, err := s.repo.GetProductByCode(ctx, code)
			if apperrors.Is(err, apperrors.NotFound) {
				// A grant for a product that does not exist grants nothing. It
				// is not an error: roles are managed in OpenAuth, and a stale
				// grant must not lock an operator out of their other products.
				continue
			}
			if err != nil {
				return nil, err
			}
			ids = append(ids, product.ID)
		}
		return filter.OnlyProducts(ids...), nil
	default:
		return filter.OnlyProducts(), nil
	}
}

// requireVisible reports an out-of-scope row as NotFound.
//
// NotFound, not PermissionDenied: telling a Zshala operator "you may not see
// this" confirms that a BappaApp record with that id exists.
func requireVisible(scope *filter.ProductScope, productID *int64, what, ref string) error {
	if !scope.Allows(productID) {
		return apperrors.New(apperrors.NotFound, "%s %q not found", what, ref)
	}
	return nil
}

// requireProductInScope guards an explicit product filter on a listing.
//
// Here the caller named the product, so a denial reveals nothing they did not
// already claim to know, and an explicit error is safer than an empty list: a
// later refactor could quietly widen an empty result, but not a refusal.
func requireProductInScope(scope *filter.ProductScope, productID int64) error {
	if !scope.AllowsProduct(productID) {
		return apperrors.New(apperrors.PermissionDenied, "that product is outside your scope")
	}
	return nil
}
