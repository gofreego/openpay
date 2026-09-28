package service_test

import (
	"context"
	"testing"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/repository/postgresql"
	"github.com/gofreego/openpay/internal/service"
	"github.com/gofreego/openpay/internal/testsupport"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

var repo *postgresql.Repository

// testService is a service over the integration-test database, freshly emptied.
func testService(t *testing.T) *service.Service {
	t.Helper()
	repo = testsupport.Repository(t)
	return service.NewService(context.Background(), &service.Config{}, repo)
}

func as(perms ...string) context.Context {
	return appcontext.WithCaller(context.Background(), appcontext.Caller{
		Kind: appcontext.KindOperator, UserID: "op_test", Permissions: perms,
		RequestID: ids.New(ids.Request), IdempotencyKey: ids.New(ids.Idempotency),
	})
}

var readPerms = []string{
	auth.PermProductsRead, auth.PermWalletTypesRead, auth.PermLedgerRead,
	auth.PermProductsWrite, auth.PermWalletTypesWrite, auth.PermLedgerCheck, auth.PermCredentialsWrite,
	auth.PermWalletsRead, auth.PermWalletsGrant,
}

func central() context.Context { return as(append(readPerms, auth.PermScopeAll)...) }

func productOps(codes ...string) context.Context {
	perms := append([]string{}, readPerms...)
	for _, code := range codes {
		perms = append(perms, auth.PermScopeProductPrefix+code)
	}
	return as(perms...)
}

func wantCode(t *testing.T, what string, err error, code apperrors.Code) {
	t.Helper()
	if !apperrors.Is(err, code) {
		t.Errorf("%s: error code = %q (%v), want %q", what, apperrors.CodeOf(err), err, code)
	}
}

type estate struct {
	svc           *service.Service
	zshala, bappa *openpay_v1.Product
}

func setupEstate(t *testing.T) estate {
	t.Helper()
	svc := testService(t)
	create := func(code string) *openpay_v1.Product {
		resp, err := svc.CreateProduct(central(), &openpay_v1.CreateProductRequest{
			Code: code, Name: code, DefaultCurrency: "INR",
		})
		if err != nil {
			t.Fatalf("create %s: %v", code, err)
		}
		return resp.GetProduct()
	}
	e := estate{svc: svc, zshala: create("zshala"), bappa: create("bappaapp")}
	if _, err := ledger.EnsureChart(context.Background(), repo, ledger.ChartConfig{Providers: []string{"razorpay"}}); err != nil {
		t.Fatalf("chart: %v", err)
	}
	return e
}

// A product operator sees their product and nothing else, and every way of
// asking for another product's data is refused explicitly — not answered with
// an empty list that a later refactor could quietly widen (plan.md U-D6).
func TestProductOperatorCannotSeeAnotherProduct(t *testing.T) {
	e := setupEstate(t)
	ctx := productOps("zshala")

	products, err := e.svc.ListProducts(ctx, &openpay_v1.ListProductsRequest{})
	if err != nil {
		t.Fatalf("list products: %v", err)
	}
	if len(products.GetProducts()) != 1 || products.GetProducts()[0].GetCode() != "zshala" || products.GetTotal() != 1 {
		t.Errorf("listed %d products (total %d), want only zshala", len(products.GetProducts()), products.GetTotal())
	}

	_, err = e.svc.GetProduct(ctx, &openpay_v1.GetProductRequest{Id: e.bappa.GetId()})
	wantCode(t, "get another product", err, apperrors.NotFound)

	_, err = e.svc.GetLedgerAccount(ctx, &openpay_v1.GetLedgerAccountRequest{Id: ledger.ProductSales("bappaapp")})
	wantCode(t, "get another product's account", err, apperrors.NotFound)

	_, err = e.svc.GetAccountStatement(ctx, &openpay_v1.GetAccountStatementRequest{AccountId: ledger.ProductSales("bappaapp")})
	wantCode(t, "another product's statement", err, apperrors.NotFound)

	_, err = e.svc.ListLedgerAccounts(ctx, &openpay_v1.ListLedgerAccountsRequest{ProductId: e.bappa.GetId()})
	wantCode(t, "list another product's accounts", err, apperrors.PermissionDenied)

	_, err = e.svc.GetTrialBalance(ctx, &openpay_v1.GetTrialBalanceRequest{ProductId: e.bappa.GetId()})
	wantCode(t, "another product's trial balance", err, apperrors.PermissionDenied)

	_, err = e.svc.ListWalletTypes(ctx, &openpay_v1.ListWalletTypesRequest{ProductId: e.bappa.GetId()})
	wantCode(t, "another product's wallet types", err, apperrors.PermissionDenied)

	// Their own product works throughout.
	own, err := e.svc.ListLedgerAccounts(ctx, &openpay_v1.ListLedgerAccountsRequest{Limit: 100})
	if err != nil {
		t.Fatalf("list own accounts: %v", err)
	}
	if len(own.GetAccounts()) == 0 {
		t.Error("product operator sees none of their own product's accounts")
	}
	for _, a := range own.GetAccounts() {
		if a.GetProductId() != e.zshala.GetId() {
			t.Errorf("unscoped listing leaked %s (product %q)", a.GetCode(), a.GetProductId())
		}
	}
	if _, err := e.svc.GetTrialBalance(ctx, &openpay_v1.GetTrialBalanceRequest{ProductId: e.zshala.GetId()}); err != nil {
		t.Errorf("own trial balance: %v", err)
	}
}

// Platform accounts and platform operations belong to no product, so no
// product scope reaches them — however many products it covers.
func TestProductOperatorCannotReachThePlatform(t *testing.T) {
	e := setupEstate(t)
	ctx := productOps("zshala", "bappaapp")

	_, err := e.svc.GetLedgerAccount(ctx, &openpay_v1.GetLedgerAccountRequest{Id: ledger.PSPReceivable("razorpay")})
	wantCode(t, "platform account", err, apperrors.NotFound)

	_, err = e.svc.ListLedgerAccounts(ctx, &openpay_v1.ListLedgerAccountsRequest{PlatformOnly: true})
	wantCode(t, "list platform accounts", err, apperrors.PermissionDenied)

	_, err = e.svc.GetTrialBalance(ctx, &openpay_v1.GetTrialBalanceRequest{})
	wantCode(t, "platform-wide trial balance", err, apperrors.PermissionDenied)

	_, err = e.svc.RunLedgerCheck(ctx, &openpay_v1.RunLedgerCheckRequest{})
	wantCode(t, "run ledger check", err, apperrors.PermissionDenied)

	_, err = e.svc.CreateProduct(ctx, &openpay_v1.CreateProductRequest{Code: "newone", Name: "New", DefaultCurrency: "INR"})
	wantCode(t, "create product", err, apperrors.PermissionDenied)

	_, err = e.svc.ListServiceCredentials(ctx, &openpay_v1.ListServiceCredentialsRequest{ProductId: e.zshala.GetId()})
	wantCode(t, "list credentials", err, apperrors.PermissionDenied)

	_, err = e.svc.CreateWalletType(ctx, &openpay_v1.CreateWalletTypeRequest{
		ProductId: e.zshala.GetId(), Code: "EXTRA", Name: "Extra", Currency: "INR",
		Capabilities: &openpay_v1.WalletCapabilities{Fundable: true},
	})
	wantCode(t, "create wallet type", err, apperrors.PermissionDenied)

	// Every account a two-product operator can list belongs to one of their products.
	all, err := e.svc.ListLedgerAccounts(ctx, &openpay_v1.ListLedgerAccountsRequest{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, a := range all.GetAccounts() {
		if a.GetProductId() == "" {
			t.Errorf("platform account %s leaked into a product-scoped listing", a.GetCode())
		}
	}
}

// An operator with verbs but no scope sees nothing: scope is granted, never assumed.
func TestOperatorWithoutScopeSeesNothing(t *testing.T) {
	e := setupEstate(t)
	ctx := as(readPerms...)

	products, err := e.svc.ListProducts(ctx, &openpay_v1.ListProductsRequest{})
	if err != nil {
		t.Fatalf("list products: %v", err)
	}
	if len(products.GetProducts()) != 0 {
		t.Errorf("operator without scope listed %d products, want 0", len(products.GetProducts()))
	}
	_, err = e.svc.GetProduct(ctx, &openpay_v1.GetProductRequest{Id: e.zshala.GetId()})
	wantCode(t, "get product without scope", err, apperrors.NotFound)
}

// A grant naming a product that does not exist grants nothing, and does not
// cost the operator their other products.
func TestStaleScopeGrantIsIgnored(t *testing.T) {
	e := setupEstate(t)
	ctx := productOps("retired_product", "zshala")

	if _, err := e.svc.GetProduct(ctx, &openpay_v1.GetProductRequest{Id: e.zshala.GetId()}); err != nil {
		t.Errorf("a stale grant broke access to a live one: %v", err)
	}
}

// Central ops see everything, platform included.
func TestCentralOpsSeeTheWholeEstate(t *testing.T) {
	e := setupEstate(t)
	ctx := central()

	products, err := e.svc.ListProducts(ctx, &openpay_v1.ListProductsRequest{})
	if err != nil || products.GetTotal() != 2 {
		t.Errorf("central ops listed %d products (%v), want 2", products.GetTotal(), err)
	}
	if _, err := e.svc.GetLedgerAccount(ctx, &openpay_v1.GetLedgerAccountRequest{Id: ledger.PSPReceivable("razorpay")}); err != nil {
		t.Errorf("central ops reading a platform account: %v", err)
	}
	if _, err := e.svc.GetTrialBalance(ctx, &openpay_v1.GetTrialBalanceRequest{}); err != nil {
		t.Errorf("central ops platform trial balance: %v", err)
	}
}

// Every product starts with MAIN and BONUS, configured so that purchased and
// granted money cannot be confused (plan.md D10).
func TestProductsStartWithMainAndBonus(t *testing.T) {
	e := setupEstate(t)

	resp, err := e.svc.ListWalletTypes(central(), &openpay_v1.ListWalletTypesRequest{ProductId: e.zshala.GetId()})
	if err != nil {
		t.Fatalf("list wallet types: %v", err)
	}
	byCode := map[string]*openpay_v1.WalletType{}
	for _, wt := range resp.GetWalletTypes() {
		byCode[wt.GetCode()] = wt
	}

	main, bonus := byCode[service.WalletTypeMain], byCode[service.WalletTypeBonus]
	if main == nil || bonus == nil {
		t.Fatalf("wallet types = %v, want MAIN and BONUS", byCode)
	}
	if c := main.GetCapabilities(); !c.GetFundable() || c.GetGrantable() || c.GetWithdrawable() ||
		main.GetExpiryPolicy() != openpay_v1.ExpiryPolicy_EXPIRY_POLICY_NONE {
		t.Errorf("MAIN = %+v, want fundable, not grantable, not withdrawable, non-expiring", main)
	}
	if c := bonus.GetCapabilities(); c.GetFundable() || !c.GetGrantable() || c.GetWithdrawable() ||
		bonus.GetExpiryPolicy() == openpay_v1.ExpiryPolicy_EXPIRY_POLICY_NONE {
		t.Errorf("BONUS = %+v, want grantable only, not withdrawable, expiring", bonus)
	}
}
