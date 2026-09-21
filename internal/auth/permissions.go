package auth

import (
	"context"

	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// Permissions an operator can hold. They arrive in x-user-perms, issued by
// OpenAuth, and are checked here — the console gating its own buttons is
// cosmetic, not a boundary (plan.md U-D6).
const (
	PermProductsRead     = "openpay:products:read"
	PermProductsWrite    = "openpay:products:write"
	PermCredentialsWrite = "openpay:credentials:write"
)

// RequireOperator asserts the caller is an operator holding permission.
//
// Endpoints call this explicitly rather than relying on an interceptor,
// because the HTTP and gRPC paths converge only at the service method: the
// gateway registers the service in-process and never runs gRPC interceptors.
// One check at the convergence point covers both, and is greppable.
func RequireOperator(ctx context.Context, permission string) error {
	caller, ok := appcontext.CallerFrom(ctx)
	if !ok {
		// No caller means the request never passed an edge. Fail closed.
		return apperrors.New(apperrors.Unauthenticated, "authentication required")
	}

	switch caller.Kind {
	case appcontext.KindOperator:
		if !caller.HasPermission(permission) {
			return apperrors.New(apperrors.PermissionDenied,
				"this action requires the %q permission", permission)
		}
		return nil
	case appcontext.KindService:
		// Deliberately distinct from a missing permission: a product backend
		// holding a valid credential is authenticated but is simply not an
		// operator, and no permission grant would change that.
		return apperrors.New(apperrors.PermissionDenied,
			"this is an operator-only endpoint; service credentials cannot call it")
	default:
		return apperrors.New(apperrors.Unauthenticated, "authentication required")
	}
}

// RequireService asserts the caller is a product backend and returns the
// product it is bound to.
//
// The product comes from the credential, never from the request, so a caller
// cannot act on a product that is not theirs by naming it.
func RequireService(ctx context.Context) (productID int64, err error) {
	caller, ok := appcontext.CallerFrom(ctx)
	if !ok || caller.Kind == appcontext.KindAnonymous {
		return 0, apperrors.New(apperrors.Unauthenticated, "authentication required")
	}
	if caller.Kind != appcontext.KindService {
		return 0, apperrors.New(apperrors.PermissionDenied,
			"this endpoint requires a service credential")
	}
	return caller.ProductID, nil
}
