package dao

import "time"

type WalletStatus string

const (
	WalletActive WalletStatus = "active"
	// WalletFrozen stops customer-initiated movement while support
	// investigates. Operator adjustments still work: they are how a frozen
	// wallet is put right.
	WalletFrozen WalletStatus = "frozen"
)

// Wallet is one customer's balance of one wallet type. It stores no balance:
// that is whatever its ledger account says (plan.md D2).
type Wallet struct {
	ID           int64
	PublicID     string
	CustomerID   int64
	WalletTypeID int64

	// ProductID is copied from the wallet type; nil for a platform-scoped type.
	ProductID *int64

	LedgerAccountID int64
	Status          WalletStatus

	CreatedAt time.Time
	UpdatedAt time.Time
}
