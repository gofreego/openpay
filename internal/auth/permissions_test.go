package auth

import (
	"context"
	"testing"

	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/pkg/apperrors"
)

func operatorCtx(perms ...string) context.Context {
	return appcontext.WithCaller(context.Background(), appcontext.Caller{
		Kind:        appcontext.KindOperator,
		UserID:      "ops_42",
		Permissions: perms,
	})
}

func serviceCtx(productID int64) context.Context {
	return appcontext.WithCaller(context.Background(), appcontext.Caller{
		Kind:      appcontext.KindService,
		ProductID: productID,
	})
}

func TestRequireOperator(t *testing.T) {
	if err := RequireOperator(operatorCtx(PermProductsWrite), PermProductsWrite); err != nil {
		t.Errorf("operator holding the permission was rejected: %v", err)
	}

	err := RequireOperator(operatorCtx(PermProductsRead), PermProductsWrite)
	if !apperrors.Is(err, apperrors.PermissionDenied) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.PermissionDenied)
	}
}

// A context that never passed an edge holds nothing, so authorization must fail
// closed rather than treating "no caller" as "no restrictions".
func TestRequireOperatorFailsClosedOnBareContext(t *testing.T) {
	err := RequireOperator(context.Background(), PermProductsWrite)
	if !apperrors.Is(err, apperrors.Unauthenticated) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.Unauthenticated)
	}
}

func TestRequireOperatorRejectsAnonymous(t *testing.T) {
	ctx := appcontext.WithCaller(context.Background(), appcontext.Caller{Kind: appcontext.KindAnonymous})
	if err := RequireOperator(ctx, PermProductsWrite); !apperrors.Is(err, apperrors.Unauthenticated) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.Unauthenticated)
	}
}

// A product backend with a valid credential is authenticated but is not an
// operator, and no permission grant would make it one.
func TestRequireOperatorRejectsServiceCredentials(t *testing.T) {
	err := RequireOperator(serviceCtx(1), PermProductsWrite)
	if !apperrors.Is(err, apperrors.PermissionDenied) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.PermissionDenied)
	}
}

func TestRequireService(t *testing.T) {
	productID, err := RequireService(serviceCtx(7))
	if err != nil {
		t.Fatalf("service caller was rejected: %v", err)
	}
	// The product must come from the credential, never from the request.
	if productID != 7 {
		t.Errorf("productID = %d, want 7", productID)
	}
}

func TestRequireServiceRejectsOperatorsAndAnonymous(t *testing.T) {
	if _, err := RequireService(operatorCtx(PermProductsWrite)); !apperrors.Is(err, apperrors.PermissionDenied) {
		t.Errorf("operator: error code = %q, want %q", apperrors.CodeOf(err), apperrors.PermissionDenied)
	}
	if _, err := RequireService(context.Background()); !apperrors.Is(err, apperrors.Unauthenticated) {
		t.Errorf("bare context: error code = %q, want %q", apperrors.CodeOf(err), apperrors.Unauthenticated)
	}
}
