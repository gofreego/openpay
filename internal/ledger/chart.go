// Package ledger holds the chart of accounts: what accounts exist, what they
// are called, and how they are created.
//
// Every account code is built here. A code assembled by string concatenation
// somewhere else is how "income:zshala:sales" and "income:zshala:product_sales"
// both end up in the ledger, each holding half the revenue.
package ledger

import (
	"context"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

// Currency is the one currency v1 operates in (plan.md D11).
const Currency = "INR"

// Platform accounts: owned by the company, no product segment, product_id NULL.
const (
	// PlatformPSPFees holds PSP charges that have no originating payment —
	// monthly minimums, chargeback handling. Per-payment fees go to the
	// product's own ProductPSPFees account instead (plan.md D12).
	PlatformPSPFees = "expense:psp_fees"

	// GSTPayable is output tax collected on the products' behalf (D13).
	GSTPayable = "liability:gst_payable"

	// InputTaxCredit is GST charged to us on PSP fees: reclaimable, so an
	// asset, and kept out of the fee expense so payment costs are not
	// overstated (D13).
	InputTaxCredit = "asset:input_tax_credit"

	OpeningBalance = "equity:opening_balance"
)

// PSPReceivable is what a provider owes us for captured payments not yet settled.
func PSPReceivable(provider string) string { return "psp:" + provider + ":receivable" }

// PSPSuspense holds provider money that reconciliation could not yet match to
// a payment. It should trend to zero; a growing balance is an open question.
func PSPSuspense(provider string) string { return "psp:" + provider + ":suspense" }

// Bank is a settlement destination account.
func Bank(bank string) string { return "bank:" + bank + ":current" }

// Product accounts: created when a product is registered, product_id set.
func ProductSales(product string) string { return "income:" + product + ":product_sales" }
func ProductFeeRecovery(product string) string {
	return "income:" + product + ":fee_recovery"
}
func ProductBreakage(product string) string { return "income:" + product + ":breakage" }
func ProductDiscounts(product string) string {
	return "expense:" + product + ":discounts"
}
func ProductPromotions(product string) string {
	return "expense:" + product + ":promotions"
}
func ProductPSPFees(product string) string { return "expense:" + product + ":psp_fees" }

// ProductAdjustments funds operator corrections and goodwill credits. Kept
// apart from promotions: an adjustment puts right something that went wrong,
// a promotion is a decision to give money away, and finance reads them
// differently.
func ProductAdjustments(product string) string {
	return "expense:" + product + ":adjustments"
}

// ProductDisputed holds contested money while a chargeback is decided: no
// longer the customer's to spend, not yet the PSP's to keep.
func ProductDisputed(product string) string { return "liability:" + product + ":disputed" }

// ProductChargebacks is what lost disputes cost when the customer had
// already spent the money: the part no wallet could give back.
func ProductChargebacks(product string) string { return "expense:" + product + ":chargebacks" }

// PlatformScope stands in for the product segment of a platform-scoped
// wallet's account code, which belongs to no product.
const PlatformScope = "platform"

// WalletAccount is the liability backing one customer's wallet of one type,
// e.g. wallet:cus_0199…:zshala:MAIN. Pass PlatformScope as product for a
// platform-scoped type. Customer public ids never contain ':', so the code
// cannot be ambiguous.
func WalletAccount(customer, product, walletType string) string {
	return "wallet:" + customer + ":" + product + ":" + walletType
}

func ProductRefundsPayable(product string) string {
	return "liability:" + product + ":refunds_payable"
}

// ChartConfig names the external parties the platform chart is built for.
type ChartConfig struct {
	// Providers are PSP codes, e.g. razorpay, cashfree. Each gets a receivable
	// and a suspense account.
	Providers []string `yaml:"Providers"`
	// Banks are settlement destinations, e.g. hdfc.
	Banks []string `yaml:"Banks"`
}

// Every chart account permits a negative balance. Overdraft protection exists
// to stop the ledger authorising spend a customer does not have, which is a
// customer-wallet concern. Chart accounts instead record what already happened
// in the world — a settlement that nets out refunds, a fee that exceeds the
// estimate — and refusing to post those does not undo them, it only loses the
// record. Drift is caught by the invariant checker and reconciliation instead.
func chartAccount(code string, accountType dao.AccountType, owner dao.AccountOwnerKind, ownerID *string, productID *int64) *dao.LedgerAccount {
	return &dao.LedgerAccount{
		PublicID:      ids.New(ids.LedgerAccount),
		Code:          code,
		ProductID:     productID,
		Type:          accountType,
		Currency:      Currency,
		OwnerKind:     owner,
		OwnerID:       ownerID,
		AllowNegative: true,
		Status:        dao.AccountActive,
	}
}

// PlatformChart is every account the company holds regardless of product.
func PlatformChart(cfg ChartConfig) []*dao.LedgerAccount {
	accounts := []*dao.LedgerAccount{
		chartAccount(PlatformPSPFees, dao.AccountExpense, dao.OwnerPlatform, nil, nil),
		chartAccount(GSTPayable, dao.AccountLiability, dao.OwnerPlatform, nil, nil),
		chartAccount(InputTaxCredit, dao.AccountAsset, dao.OwnerPlatform, nil, nil),
		chartAccount(OpeningBalance, dao.AccountEquity, dao.OwnerPlatform, nil, nil),
	}
	for _, provider := range cfg.Providers {
		accounts = append(accounts,
			chartAccount(PSPReceivable(provider), dao.AccountAsset, dao.OwnerProvider, &provider, nil),
			chartAccount(PSPSuspense(provider), dao.AccountAsset, dao.OwnerProvider, &provider, nil),
		)
	}
	for _, bank := range cfg.Banks {
		accounts = append(accounts, chartAccount(Bank(bank), dao.AccountAsset, dao.OwnerPlatform, nil, nil))
	}
	return accounts
}

// ProductChart is every account a product needs from the moment it exists.
// Revenue, promotion cost and PSP fees are all attributed to the product at
// the finest grain; rolling them up is a report, splitting them back out
// would be a reconstruction (D12).
func ProductChart(product *dao.Product) []*dao.LedgerAccount {
	code, owner, id := product.Code, &product.PublicID, &product.ID
	return []*dao.LedgerAccount{
		chartAccount(ProductSales(code), dao.AccountIncome, dao.OwnerMerchant, owner, id),
		chartAccount(ProductFeeRecovery(code), dao.AccountIncome, dao.OwnerMerchant, owner, id),
		chartAccount(ProductBreakage(code), dao.AccountIncome, dao.OwnerMerchant, owner, id),
		chartAccount(ProductDiscounts(code), dao.AccountExpense, dao.OwnerMerchant, owner, id),
		chartAccount(ProductPromotions(code), dao.AccountExpense, dao.OwnerMerchant, owner, id),
		chartAccount(ProductPSPFees(code), dao.AccountExpense, dao.OwnerMerchant, owner, id),
		chartAccount(ProductRefundsPayable(code), dao.AccountLiability, dao.OwnerMerchant, owner, id),
		chartAccount(ProductAdjustments(code), dao.AccountExpense, dao.OwnerMerchant, owner, id),
		chartAccount(ProductDisputed(code), dao.AccountLiability, dao.OwnerMerchant, owner, id),
		chartAccount(ProductChargebacks(code), dao.AccountExpense, dao.OwnerMerchant, owner, id),
	}
}

// Repository is what building the chart needs.
type Repository interface {
	GetOrCreateLedgerAccount(ctx context.Context, account *dao.LedgerAccount) (created bool, err error)
	ListProducts(ctx context.Context, f *filter.Product) ([]*dao.Product, int64, error)
}

// EnsureAccounts creates any of accounts that do not exist yet. It is safe to
// run repeatedly and concurrently.
//
// An existing account that disagrees with the chart — a different type,
// currency or product — is a FailedPrecondition, never silently accepted: postings made
// on the assumption that income:x is income would be wrong in every report if
// it were in fact a liability.
func EnsureAccounts(ctx context.Context, repo Repository, accounts []*dao.LedgerAccount) (created int, err error) {
	for _, want := range accounts {
		got := *want
		isNew, err := repo.GetOrCreateLedgerAccount(ctx, &got)
		if err != nil {
			return created, err
		}
		if isNew {
			created++
			continue
		}
		if got.Type != want.Type || got.Currency != want.Currency || !sameProduct(got.ProductID, want.ProductID) {
			return created, apperrors.New(apperrors.FailedPrecondition,
				"ledger account %q exists as a %s %s account for product %v, but the chart defines it as %s %s for product %v",
				want.Code, got.Currency, got.Type, deref(got.ProductID), want.Currency, want.Type, deref(want.ProductID))
		}
	}
	return created, nil
}

// EnsureChart brings the whole chart up to date: the platform accounts, and
// every product's accounts. Products registered from now on get theirs at
// registration; this covers the ones that existed before, and any provider or
// bank added to the configuration.
func EnsureChart(ctx context.Context, repo Repository, cfg ChartConfig) (created int, err error) {
	created, err = EnsureAccounts(ctx, repo, PlatformChart(cfg))
	if err != nil {
		return created, err
	}

	// Offset paging is safe here: listing is newest-first and products are
	// never deleted, so a product registered mid-walk only pushes rows down a
	// page — one is visited twice, which is harmless, and none is skipped.
	f := &filter.Product{Limit: 100, Scope: filter.AllProducts()}
	for {
		products, _, err := repo.ListProducts(ctx, f)
		if err != nil {
			return created, err
		}
		for _, product := range products {
			n, err := EnsureAccounts(ctx, repo, ProductChart(product))
			created += n
			if err != nil {
				return created, err
			}
		}
		if len(products) < f.Limit {
			return created, nil
		}
		f.Offset += len(products)
	}
}

func sameProduct(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func deref(v *int64) any {
	if v == nil {
		return "none"
	}
	return *v
}
