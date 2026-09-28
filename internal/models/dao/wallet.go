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

	// CustomerPublicID is read alongside, for events and the API.
	CustomerPublicID string

	// ProductID is copied from the wallet type; nil for a platform-scoped type.
	ProductID *int64

	LedgerAccountID int64
	Status          WalletStatus

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ExpiryCandidate is a wallet whose rolling expiry has passed: it has a
// balance, no active holds, and no posting for longer than its type allows.
type ExpiryCandidate struct {
	WalletPublicID string
	// LastPostingID is the most recent activity seen. Expiry is keyed on it,
	// so each dormant period lapses at most once, and any activity since the
	// candidate was found cancels the expiry.
	LastPostingID int64
	Balance       int64
}
