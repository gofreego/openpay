package dao

import "time"

type WalletScope string

const (
	// WalletScopeProduct confines a balance to one product.
	WalletScopeProduct WalletScope = "product"
	// WalletScopePlatform makes one balance spendable across every product —
	// possible only because this is a single-tenant estate.
	WalletScopePlatform WalletScope = "platform"
)

type ExpiryPolicy string

const (
	ExpiryNone ExpiryPolicy = "none"
	// ExpiryFixed expires a balance a fixed period after it was credited.
	ExpiryFixed ExpiryPolicy = "fixed"
	// ExpiryRolling pushes expiry out on every use.
	ExpiryRolling ExpiryPolicy = "rolling"
)

type WalletTypeStatus string

const (
	WalletTypeActive WalletTypeStatus = "active"
	// WalletTypeArchived stops new wallets being created from this type.
	// Existing wallets keep working: they hold real balances.
	WalletTypeArchived WalletTypeStatus = "archived"
)

// WalletType is the configuration a balance behaves by. The wallet engine has
// no per-product special cases; a "BONUS" wallet is only a row with different
// capabilities from a "MAIN" one.
type WalletType struct {
	ID       int64
	PublicID string

	// ProductID is nil for platform-scoped types.
	ProductID *int64
	// ProductPublicID is the product's public id, read alongside the type so
	// the API can name the owning product without a second lookup. Nil for
	// platform-scoped types.
	ProductPublicID *string
	Scope           WalletScope

	Code     string
	Name     string
	Currency string

	// Fundable: may be topped up with real money. Grantable: may be credited
	// without payment, as a promotion.
	//
	// Keeping these apart is what lets "how much real customer money do we
	// hold?" stay answerable, which is the question compliance and finance
	// always ask.
	Fundable  bool
	Grantable bool

	// Withdrawable turns a closed-loop balance into a prepaid instrument, so it
	// carries a recorded approval rather than being a bare flag.
	Withdrawable bool

	Transferable       bool
	RefundableToSource bool
	AllowNegative      bool

	ExpiryPolicy ExpiryPolicy
	ExpiryDays   *int

	// Limits in minor units. nil means no limit.
	MaxBalance     *int64
	MaxTxnAmount   *int64
	DailyLoadLimit *int64

	Status WalletTypeStatus

	WithdrawableApprovedBy  *string
	WithdrawableApprovedAt  *time.Time
	WithdrawableApprovalRef *string

	CreatedAt time.Time
	UpdatedAt time.Time
}

type CustomerStatus string

const (
	CustomerActive  CustomerStatus = "active"
	CustomerBlocked CustomerStatus = "blocked"
	// CustomerMerged marks a duplicate that now resolves to another record.
	// The row stays so its external_ref keeps resolving to the survivor.
	CustomerMerged CustomerStatus = "merged"
)

// Customer is a person. There is no product_id: every product identifies people
// by the same OpenAuth user id, so one row covers them everywhere.
type Customer struct {
	ID       int64
	PublicID string

	// ExternalRef is the OpenAuth user id.
	ExternalRef string

	Status               CustomerStatus
	MergedIntoCustomerID *int64

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (c *Customer) IsMerged() bool { return c.Status == CustomerMerged }
