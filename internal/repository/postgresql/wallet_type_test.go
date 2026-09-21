package postgresql

import (
	"context"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

func truncateWallets(t *testing.T, repo *Repository) {
	t.Helper()
	if _, err := repo.connManager.Primary().ExecContext(context.Background(),
		"TRUNCATE wallet_types, customers, audit_log, service_credentials, products RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate wallet tables (have migrations run?): %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

// mainType is the canonical fundable, closed-loop wallet.
func mainType(productID int64) *dao.WalletType {
	return &dao.WalletType{
		PublicID: ids.New(ids.WalletType), ProductID: &productID, Scope: dao.WalletScopeProduct,
		Code: "MAIN", Name: "Main balance", Currency: "INR",
		Fundable: true, Grantable: false, Withdrawable: false,
		Transferable: false, RefundableToSource: true, AllowNegative: false,
		ExpiryPolicy: dao.ExpiryNone, Status: dao.WalletTypeActive,
	}
}

// bonusType is the canonical granted, expiring, non-withdrawable wallet.
func bonusType(productID int64) *dao.WalletType {
	return &dao.WalletType{
		PublicID: ids.New(ids.WalletType), ProductID: &productID, Scope: dao.WalletScopeProduct,
		Code: "BONUS", Name: "Bonus coins", Currency: "INR",
		Fundable: false, Grantable: true, Withdrawable: false,
		Transferable: false, RefundableToSource: false, AllowNegative: false,
		ExpiryPolicy: dao.ExpiryFixed, ExpiryDays: ptr(90), Status: dao.WalletTypeActive,
	}
}

func TestCreateAndGetWalletType(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)
	ctx := context.Background()

	product := createProduct(t, repo, "zshala")
	walletType := mainType(product.ID)
	if err := repo.CreateWalletType(ctx, walletType); err != nil {
		t.Fatalf("create wallet type: %v", err)
	}

	loaded, err := repo.GetWalletTypeByPublicID(ctx, walletType.PublicID)
	if err != nil {
		t.Fatalf("load wallet type: %v", err)
	}
	if loaded.Code != "MAIN" || !loaded.Fundable || loaded.Grantable {
		t.Errorf("capabilities did not round-trip: %+v", loaded)
	}
}

// D10's rules are enforced in the database, not only in code, so they hold even
// if something writes around the service layer.
func TestWalletTypeRejectsIncoherentCapabilities(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)
	ctx := context.Background()
	product := createProduct(t, repo, "zshala")

	cases := []struct {
		name   string
		mutate func(*dao.WalletType)
		why    string
	}{
		{
			name: "withdrawable without fundable",
			mutate: func(w *dao.WalletType) {
				w.Fundable, w.Grantable, w.Withdrawable = false, true, true
				w.WithdrawableApprovedBy, w.WithdrawableApprovedAt = ptr("ops_42"), ptr(time.Now())
			},
			why: "cashing out money nobody paid in",
		},
		{
			name: "withdrawable without recorded approval",
			mutate: func(w *dao.WalletType) {
				w.Withdrawable = true
			},
			why: "withdrawal enabled with no compliance sign-off",
		},
		{
			name: "neither fundable nor grantable",
			mutate: func(w *dao.WalletType) {
				w.Fundable, w.Grantable = false, false
			},
			why: "a balance that can never be non-zero",
		},
		{
			name: "expiry policy without a period",
			mutate: func(w *dao.WalletType) {
				w.ExpiryPolicy, w.ExpiryDays = dao.ExpiryFixed, nil
			},
			why: "an expiry policy that never expires anything",
		},
		{
			name: "expiry days without a policy",
			mutate: func(w *dao.WalletType) {
				w.ExpiryPolicy, w.ExpiryDays = dao.ExpiryNone, ptr(30)
			},
			why: "a period that nothing applies",
		},
		{
			name: "product scope with no product",
			mutate: func(w *dao.WalletType) {
				w.ProductID = nil
			},
			why: "scope and product_id disagreeing",
		},
		{
			name: "platform scope with a product",
			mutate: func(w *dao.WalletType) {
				w.Scope = dao.WalletScopePlatform
			},
			why: "scope and product_id disagreeing",
		},
		{
			name: "non-positive limit",
			mutate: func(w *dao.WalletType) {
				w.MaxBalance = ptr(int64(0))
			},
			why: "a limit of zero would block every operation",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			walletType := mainType(product.ID)
			walletType.PublicID = ids.New(ids.WalletType)
			walletType.Code = "T" + tc.name[:3]
			tc.mutate(walletType)

			if err := repo.CreateWalletType(ctx, walletType); err == nil {
				t.Errorf("database accepted %s (%s)", tc.name, tc.why)
			}
		})
	}
}

func TestWalletTypeAcceptsWithdrawableWithApproval(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)
	ctx := context.Background()
	product := createProduct(t, repo, "zshala")

	walletType := mainType(product.ID)
	walletType.Withdrawable = true
	walletType.WithdrawableApprovedBy = ptr("ops_42")
	walletType.WithdrawableApprovedAt = ptr(time.Now())
	walletType.WithdrawableApprovalRef = ptr("COMPLIANCE-2026-01")

	if err := repo.CreateWalletType(ctx, walletType); err != nil {
		t.Fatalf("a withdrawable type with recorded approval should be accepted: %v", err)
	}

	loaded, err := repo.GetWalletTypeByPublicID(ctx, walletType.PublicID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.WithdrawableApprovedBy == nil || *loaded.WithdrawableApprovedBy != "ops_42" {
		t.Error("the approval record did not persist; who signed off must stay answerable")
	}
}

// PostgreSQL treats NULLs as distinct, so a plain UNIQUE (product_id, code)
// would allow two platform-wide types with the same code.
func TestWalletTypeCodeUniqueness(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)
	ctx := context.Background()

	zshala := createProduct(t, repo, "zshala")
	bappa := createProduct(t, repo, "bappaapp")

	if err := repo.CreateWalletType(ctx, mainType(zshala.ID)); err != nil {
		t.Fatalf("first MAIN: %v", err)
	}

	t.Run("same code in the same product is rejected", func(t *testing.T) {
		err := repo.CreateWalletType(ctx, mainType(zshala.ID))
		if !apperrors.Is(err, apperrors.AlreadyExists) {
			t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.AlreadyExists)
		}
	})

	t.Run("same code in another product is fine", func(t *testing.T) {
		if err := repo.CreateWalletType(ctx, mainType(bappa.ID)); err != nil {
			t.Errorf("MAIN in a second product should be allowed: %v", err)
		}
	})

	t.Run("platform codes are unique among themselves", func(t *testing.T) {
		platform := mainType(zshala.ID)
		platform.ProductID, platform.Scope, platform.Code = nil, dao.WalletScopePlatform, "SHARED"
		if err := repo.CreateWalletType(ctx, platform); err != nil {
			t.Fatalf("first platform type: %v", err)
		}

		duplicate := mainType(zshala.ID)
		duplicate.PublicID = ids.New(ids.WalletType)
		duplicate.ProductID, duplicate.Scope, duplicate.Code = nil, dao.WalletScopePlatform, "SHARED"
		if err := repo.CreateWalletType(ctx, duplicate); !apperrors.Is(err, apperrors.AlreadyExists) {
			t.Errorf("two platform types named SHARED were allowed: err = %v", err)
		}
	})
}

// A product's wallet options are its own types plus the platform-wide ones.
func TestListWalletTypesIncludesPlatformScoped(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)
	ctx := context.Background()

	zshala := createProduct(t, repo, "zshala")
	bappa := createProduct(t, repo, "bappaapp")

	if err := repo.CreateWalletType(ctx, mainType(zshala.ID)); err != nil {
		t.Fatalf("zshala MAIN: %v", err)
	}
	if err := repo.CreateWalletType(ctx, bonusType(bappa.ID)); err != nil {
		t.Fatalf("bappaapp BONUS: %v", err)
	}
	platform := mainType(zshala.ID)
	platform.ProductID, platform.Scope, platform.Code = nil, dao.WalletScopePlatform, "SHARED"
	if err := repo.CreateWalletType(ctx, platform); err != nil {
		t.Fatalf("platform type: %v", err)
	}

	types, err := repo.ListWalletTypes(ctx, zshala.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	codes := map[string]bool{}
	for _, wt := range types {
		codes[wt.Code] = true
	}
	if !codes["MAIN"] {
		t.Error("the product's own type is missing")
	}
	if !codes["SHARED"] {
		t.Error("the platform-scoped type is missing; it is spendable here too")
	}
	if codes["BONUS"] {
		t.Error("another product's type leaked into the listing")
	}
}

// Capabilities must not be rewritten under balances already held by those
// rules: a customer told their coins never expire must not wake up to an
// expiry policy.
func TestUpdateWalletTypeCannotChangeCapabilities(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)
	ctx := context.Background()

	product := createProduct(t, repo, "zshala")
	original := mainType(product.ID)
	if err := repo.CreateWalletType(ctx, original); err != nil {
		t.Fatalf("create: %v", err)
	}

	attempt := &dao.WalletType{
		PublicID: original.PublicID,
		Name:     "Renamed",
		Status:   dao.WalletTypeArchived,
		// These are not columns the update touches.
		Withdrawable: true,
		Grantable:    true,
		Currency:     "USD",
		ExpiryPolicy: dao.ExpiryFixed,
		MaxBalance:   ptr(int64(500000)),
	}
	if err := repo.UpdateWalletType(ctx, attempt); err != nil {
		t.Fatalf("update: %v", err)
	}

	reloaded, err := repo.GetWalletTypeByPublicID(ctx, original.PublicID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if reloaded.Name != "Renamed" || reloaded.Status != dao.WalletTypeArchived {
		t.Errorf("the mutable fields did not change: %+v", reloaded)
	}
	if reloaded.MaxBalance == nil || *reloaded.MaxBalance != 500000 {
		t.Error("limits should be adjustable")
	}
	if reloaded.Withdrawable {
		t.Error("withdrawable was changed by an update — this must require a new type")
	}
	if reloaded.Grantable {
		t.Error("grantable was changed by an update")
	}
	if reloaded.Currency != "INR" {
		t.Errorf("currency = %q, want it immutable", reloaded.Currency)
	}
	if reloaded.ExpiryPolicy != dao.ExpiryNone {
		t.Errorf("expiry policy = %q, want it immutable", reloaded.ExpiryPolicy)
	}
}
