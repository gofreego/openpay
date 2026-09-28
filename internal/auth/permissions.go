package auth

import (
	"context"
	"strings"

	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// Permissions an operator can hold. They arrive in x-user-perms, issued by
// OpenAuth, and are checked here — the console gating its own buttons is
// cosmetic, not a boundary (plan.md U-D6).
// Product scope: which products an operator's other permissions apply to.
// Verbs and scope are separate on purpose — "may read payments" and "for
// which products" are independent grants, and combining them into
// openpay:zshala:payments:read would multiply the permission list by every
// product (plan.md U-D6).
const (
	// PermScopeAll is central ops: every product, plus the platform-level
	// accounts and operations that belong to no product.
	PermScopeAll = "openpay:scope:all"
	// PermScopeProductPrefix + a product code grants that one product. An
	// operator may hold several. Codes rather than ids, because codes are
	// what a person assigning roles in OpenAuth can read.
	PermScopeProductPrefix = "openpay:scope:product:"
)

const (
	PermProductsRead     = "openpay:products:read"
	PermProductsWrite    = "openpay:products:write"
	PermCredentialsWrite = "openpay:credentials:write"

	PermWalletTypesRead  = "openpay:wallet_types:read"
	PermWalletTypesWrite = "openpay:wallet_types:write"
	// PermWalletTypesApproveWithdrawal is separate from configuring a wallet
	// type on purpose. Enabling withdrawal moves the company from closed-loop
	// into prepaid-instrument territory (plan.md D10, Q1), so it is a distinct
	// decision that a distinct person signs off on.
	PermWalletTypesApproveWithdrawal = "openpay:wallet_types:approve_withdrawal"

	PermCustomersRead = "openpay:customers:read"

	PermPaymentsRead = "openpay:payments:read"
	// PermPaymentsSync asks a provider for a payment's state and applies it.
	// It moves money only as the provider says it moved, so it is safe to
	// give product operators.
	PermPaymentsSync = "openpay:payments:sync"
	// PermRefundsCreate returns money to a card or bank. Refunding a wallet
	// top-up is close to cashing out a closed-loop balance, so it is
	// platform-level and reason-coded until orders give products their own.
	PermRefundsCreate = "openpay:refunds:create"
	// PermDisputesManage answers chargebacks with evidence: platform-level,
	// since a dispute response speaks for the company to the card network.
	PermDisputesManage = "openpay:disputes:manage"

	// Reconciliation spans every product's money, so both are platform-level.
	PermReconRead   = "openpay:recon:read"
	PermReconManage = "openpay:recon:manage"

	PermWalletsRead  = "openpay:wallets:read"
	PermWalletsGrant = "openpay:wallets:grant"
	// PermWalletsAdjust moves money that no payment or grant explains, so it
	// is platform-level: adjustments stay with central ops (plan.md U-D6).
	PermWalletsAdjust = "openpay:wallets:adjust"

	// PermLedgerRead covers accounts, statements, journals, the trial balance
	// and check history. It sees platform accounts, so until operator product
	// scope exists (plan.md U-D6) it is a central-ops permission.
	PermLedgerRead = "openpay:ledger:read"
	// PermLedgerCheck runs the invariant checks on demand. They read the whole
	// ledger in one snapshot, which is not free, hence its own permission.
	PermLedgerCheck = "openpay:ledger:check"
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

// OperatorScope describes the product scope an operator's permissions grant:
// every product, or the listed product codes. It says nothing about whether
// those codes exist; resolving them is the caller's job.
func OperatorScope(caller appcontext.Caller) (all bool, productCodes []string) {
	for _, p := range caller.Permissions {
		if p == PermScopeAll {
			return true, nil
		}
		if code, ok := strings.CutPrefix(p, PermScopeProductPrefix); ok && code != "" {
			productCodes = append(productCodes, code)
		}
	}
	return false, productCodes
}

// RequirePlatformOperator asserts an operator holding permission and the
// whole-estate scope. For operations that are platform-level by nature —
// registering products, configuring wallet types, credentials, platform
// ledger accounts — which no product-scoped operator may perform whatever
// verbs they hold.
func RequirePlatformOperator(ctx context.Context, permission string) error {
	if err := RequireOperator(ctx, permission); err != nil {
		return err
	}
	caller, _ := appcontext.CallerFrom(ctx)
	if all, _ := OperatorScope(caller); !all {
		return apperrors.New(apperrors.PermissionDenied,
			"this is a platform-level action and requires the %q scope", PermScopeAll)
	}
	return nil
}
