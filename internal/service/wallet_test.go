package service_test

import (
	"context"
	"testing"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

// backend is a product backend calling with its service credential.
func backend(t *testing.T, e estate, product *openpay_v1.Product) context.Context {
	t.Helper()
	p, err := repo.GetProductByPublicID(context.Background(), product.GetId())
	if err != nil {
		t.Fatalf("product: %v", err)
	}
	return appcontext.WithCaller(context.Background(), appcontext.Caller{
		Kind: appcontext.KindService, ProductID: p.ID, ProductCode: p.Code,
		CredentialID: "scr_test_" + p.Code, RequestID: ids.New(ids.Request),
		IdempotencyKey: ids.New(ids.Idempotency),
	})
}

// withKey gives a call its own idempotency key, as a real client would per action.
func withKey(ctx context.Context, key string) context.Context {
	caller, _ := appcontext.CallerFrom(ctx)
	caller.IdempotencyKey = key
	return appcontext.WithCaller(ctx, caller)
}

type walletEstate struct {
	estate
	customer                 string
	zshalaMain, bappaMain    *openpay_v1.Wallet
	zshalaBonus, platformPts *openpay_v1.Wallet
}

// setupWallets gives one person wallets in two products and a platform
// wallet: the shape in which a careless query leaks (plan.md Part II).
func setupWallets(t *testing.T) walletEstate {
	t.Helper()
	e := setupEstate(t)
	if _, err := e.svc.CreateWalletType(central(), &openpay_v1.CreateWalletTypeRequest{
		Code: "POINTS", Name: "Points", Currency: "INR",
		Capabilities: &openpay_v1.WalletCapabilities{Grantable: true},
		ExpiryPolicy: openpay_v1.ExpiryPolicy_EXPIRY_POLICY_NONE,
	}); err != nil {
		t.Fatalf("platform wallet type: %v", err)
	}

	zctx, bctx := backend(t, e, e.zshala), backend(t, e, e.bappa)
	cust, err := e.svc.UpsertCustomer(zctx, &openpay_v1.UpsertCustomerRequest{ExternalRef: "openauth|42"})
	if err != nil {
		t.Fatalf("customer: %v", err)
	}
	open := func(ctx context.Context, code string) *openpay_v1.Wallet {
		resp, err := e.svc.OpenWallet(ctx, &openpay_v1.OpenWalletRequest{
			CustomerId: cust.GetCustomer().GetId(), WalletTypeCode: code})
		if err != nil {
			t.Fatalf("open %s: %v", code, err)
		}
		return resp.GetWallet()
	}
	return walletEstate{
		estate: e, customer: cust.GetCustomer().GetId(),
		zshalaMain: open(zctx, "MAIN"), zshalaBonus: open(zctx, "BONUS"),
		bappaMain: open(bctx, "MAIN"), platformPts: open(zctx, "POINTS"),
	}
}

func ids_(wallets []*openpay_v1.Wallet) map[string]bool {
	out := map[string]bool{}
	for _, w := range wallets {
		out[w.GetId()] = true
	}
	return out
}

// The one place a platform-wide customer can leak one product's balances into
// another product's app. Zshala's backend asking about this person must get
// Zshala's wallets and platform ones — never BappaApp's — by any route.
func TestProductBackendSeesOnlyItsOwnWallets(t *testing.T) {
	w := setupWallets(t)
	zctx := backend(t, w.estate, w.zshala)

	listed, err := w.svc.ListCustomerWallets(zctx, &openpay_v1.ListCustomerWalletsRequest{CustomerId: w.customer})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := ids_(listed.GetWallets())
	if !got[w.zshalaMain.GetId()] || !got[w.zshalaBonus.GetId()] || !got[w.platformPts.GetId()] || len(got) != 3 {
		t.Errorf("zshala listed %v, want its MAIN and BONUS plus the platform wallet", got)
	}
	if got[w.bappaMain.GetId()] {
		t.Fatal("zshala's backend was shown BappaApp's wallet")
	}

	_, err = w.svc.GetWallet(zctx, &openpay_v1.GetWalletRequest{Id: w.bappaMain.GetId()})
	wantCode(t, "get another product's wallet", err, apperrors.NotFound)

	_, err = w.svc.GetWalletStatement(zctx, &openpay_v1.GetWalletStatementRequest{WalletId: w.bappaMain.GetId()})
	wantCode(t, "another product's statement", err, apperrors.NotFound)

	_, err = w.svc.GrantWallet(zctx, &openpay_v1.GrantWalletRequest{WalletId: w.bappaMain.GetId(), Amount: 100, ReasonCode: "promotion"})
	wantCode(t, "grant into another product's wallet", err, apperrors.NotFound)

	_, err = w.svc.TransferWallet(zctx, &openpay_v1.TransferWalletRequest{FromWalletId: w.bappaMain.GetId(), ToCustomerId: w.customer, Amount: 1})
	wantCode(t, "transfer out of another product's wallet", err, apperrors.NotFound)
}

// A product operator sees their product's wallets, but not platform wallets,
// which belong to no one product.
func TestProductOperatorWalletScope(t *testing.T) {
	w := setupWallets(t)
	ctx := as(auth.PermWalletsRead, auth.PermScopeProductPrefix+"zshala")

	listed, err := w.svc.ListCustomerWallets(ctx, &openpay_v1.ListCustomerWalletsRequest{CustomerId: w.customer})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := ids_(listed.GetWallets())
	if len(got) != 2 || !got[w.zshalaMain.GetId()] || !got[w.zshalaBonus.GetId()] {
		t.Errorf("product operator listed %v, want zshala's MAIN and BONUS only", got)
	}
	_, err = w.svc.GetWallet(ctx, &openpay_v1.GetWalletRequest{Id: w.platformPts.GetId()})
	wantCode(t, "product operator reading a platform wallet", err, apperrors.NotFound)

	all, err := w.svc.ListCustomerWallets(as(auth.PermWalletsRead, auth.PermScopeAll),
		&openpay_v1.ListCustomerWalletsRequest{CustomerId: w.customer})
	if err != nil || len(all.GetWallets()) != 4 {
		t.Errorf("central ops listed %d wallets (%v), want all 4", len(all.GetWallets()), err)
	}
}

// A retried grant with the same Idempotency-Key credits once; the float held
// counts purchased money only, so the grant does not appear in it.
func TestGrantThroughTheAPI(t *testing.T) {
	w := setupWallets(t)
	zctx := withKey(backend(t, w.estate, w.zshala), "grant-welcome-42")

	req := &openpay_v1.GrantWalletRequest{WalletId: w.zshalaBonus.GetId(), Amount: 5000, ReasonCode: "promotion", Memo: "welcome"}
	first, err := w.svc.GrantWallet(zctx, req)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	again, err := w.svc.GrantWallet(zctx, req)
	if err != nil {
		t.Fatalf("retried grant: %v", err)
	}
	if again.GetJournalId() != first.GetJournalId() || again.GetWallet().GetBalance().GetBalance() != 5000 {
		t.Errorf("retry returned journal %s with balance %d; want the original %s at 5000",
			again.GetJournalId(), again.GetWallet().GetBalance().GetBalance(), first.GetJournalId())
	}

	_, err = w.svc.GrantWallet(withKey(zctx, "grant-2"), &openpay_v1.GrantWalletRequest{
		WalletId: w.zshalaMain.GetId(), Amount: 100, ReasonCode: "promotion"})
	wantCode(t, "grant into MAIN", err, apperrors.WalletOperationDenied)

	float, err := w.svc.GetFloatHeld(central(), &openpay_v1.GetFloatHeldRequest{})
	if err != nil {
		t.Fatalf("float: %v", err)
	}
	for _, line := range float.GetLines() {
		if line.GetAmount() != 0 {
			t.Errorf("float for product %q = %d, want 0 — granted value is not customer money", line.GetProductId(), line.GetAmount())
		}
	}
}

// Adjustments are central-only, explained and reason-coded; a product
// operator holding the verb still cannot make one.
func TestAdjustIsCentralOnly(t *testing.T) {
	w := setupWallets(t)
	req := &openpay_v1.AdjustWalletRequest{WalletId: w.zshalaMain.GetId(), Amount: 700,
		Direction:  openpay_v1.PostingDirection_POSTING_DIRECTION_CREDIT,
		ReasonCode: "goodwill", Memo: "delayed delivery, ticket 881"}

	_, err := w.svc.AdjustWallet(as(auth.PermWalletsAdjust, auth.PermScopeProductPrefix+"zshala"), req)
	wantCode(t, "product operator adjusting", err, apperrors.PermissionDenied)

	resp, err := w.svc.AdjustWallet(withKey(as(auth.PermWalletsAdjust, auth.PermScopeAll), "adj-881"), req)
	if err != nil {
		t.Fatalf("central adjustment: %v", err)
	}
	if got := resp.GetWallet().GetBalance().GetBalance(); got != 700 {
		t.Errorf("balance after adjustment = %d, want 700", got)
	}

	// MAIN is fundable, so an adjustment into it is real customer money and
	// shows up in the float.
	float, err := w.svc.GetFloatHeld(central(), &openpay_v1.GetFloatHeldRequest{})
	if err != nil {
		t.Fatalf("float: %v", err)
	}
	var zshalaFloat int64
	for _, line := range float.GetLines() {
		if line.GetProductId() == w.zshala.GetId() {
			zshalaFloat = line.GetAmount()
		}
	}
	if zshalaFloat != 700 {
		t.Errorf("zshala float = %d, want 700", zshalaFloat)
	}
}

// Withdrawal policy lives on withdrawable types only, and can be tightened
// after creation like any other limit.
func TestWithdrawalPolicyConfiguration(t *testing.T) {
	e := setupEstate(t)
	approver := as(append(readPerms, auth.PermScopeAll, auth.PermWalletTypesApproveWithdrawal)...)
	req := &openpay_v1.CreateWalletTypeRequest{
		ProductId: e.zshala.GetId(), Code: "CASH", Name: "Cash", Currency: "INR",
		Capabilities:          &openpay_v1.WalletCapabilities{Fundable: true, Withdrawable: true},
		ExpiryPolicy:          openpay_v1.ExpiryPolicy_EXPIRY_POLICY_NONE,
		WithdrawalApprovalRef: "COMPLIANCE-2026-17",
		Limits:                &openpay_v1.WalletLimits{MinWithdrawalAmount: 10000, WithdrawalApprovalThreshold: 500000},
	}
	created, err := e.svc.CreateWalletType(withKey(approver, "wt-create"), req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if l := created.GetWalletType().GetLimits(); l.GetMinWithdrawalAmount() != 10000 || l.GetWithdrawalApprovalThreshold() != 500000 {
		t.Errorf("limits = %+v, want the withdrawal policy stored", l)
	}

	updated, err := e.svc.UpdateWalletType(withKey(approver, "wt-update"), &openpay_v1.UpdateWalletTypeRequest{
		Id: created.GetWalletType().GetId(), Name: "Cash", Status: openpay_v1.WalletTypeStatus_WALLET_TYPE_STATUS_ACTIVE,
		Limits: &openpay_v1.WalletLimits{MinWithdrawalAmount: 20000, WithdrawalApprovalThreshold: 200000},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := updated.GetWalletType().GetLimits().GetWithdrawalApprovalThreshold(); got != 200000 {
		t.Errorf("threshold after update = %d, want 200000", got)
	}

	// On a closed-loop type, withdrawal settings would read like permission.
	_, err = e.svc.CreateWalletType(central(), &openpay_v1.CreateWalletTypeRequest{
		ProductId: e.zshala.GetId(), Code: "CLOSED", Name: "Closed", Currency: "INR",
		Capabilities: &openpay_v1.WalletCapabilities{Fundable: true},
		ExpiryPolicy: openpay_v1.ExpiryPolicy_EXPIRY_POLICY_NONE,
		Limits:       &openpay_v1.WalletLimits{MinWithdrawalAmount: 100},
	})
	wantCode(t, "withdrawal policy on a non-withdrawable type", err, apperrors.InvalidArgument)

	bad := req
	bad.Code = "CASH2"
	bad.Limits = &openpay_v1.WalletLimits{MaxTxnAmount: 5000, MinWithdrawalAmount: 10000}
	_, err = e.svc.CreateWalletType(withKey(approver, "wt-bad"), bad)
	wantCode(t, "minimum above the per-transaction limit", err, apperrors.InvalidArgument)
}
