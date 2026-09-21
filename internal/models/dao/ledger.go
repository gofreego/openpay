package dao

import "time"

// AccountType determines which direction is "natural" for an account, and so
// whether a positive raw balance means the account holds value or owes it.
type AccountType string

const (
	AccountAsset     AccountType = "asset"
	AccountLiability AccountType = "liability"
	AccountEquity    AccountType = "equity"
	AccountIncome    AccountType = "income"
	AccountExpense   AccountType = "expense"
)

// NormalSign converts a raw balance into the human-facing one.
//
// Assets and expenses grow by debits, so their raw sum already reads correctly.
// Liabilities, income and equity grow by credits, so theirs is negated. Keeping
// this out of the posting engine is what lets the engine just add integers.
func (t AccountType) NormalSign() int64 {
	switch t {
	case AccountAsset, AccountExpense:
		return 1
	default:
		return -1
	}
}

type AccountOwnerKind string

const (
	OwnerPlatform AccountOwnerKind = "platform"
	OwnerCustomer AccountOwnerKind = "customer"
	OwnerProvider AccountOwnerKind = "provider"
	OwnerMerchant AccountOwnerKind = "merchant"
)

type AccountStatus string

const (
	AccountActive AccountStatus = "active"
	AccountClosed AccountStatus = "closed"
)

// Direction is the multiplier a posting contributes with.
type Direction int16

const (
	Debit  Direction = 1
	Credit Direction = -1
)

func (d Direction) String() string {
	if d == Debit {
		return "debit"
	}
	return "credit"
}

type LedgerAccount struct {
	ID        int64
	PublicID  string
	Code      string
	ProductID *int64
	Type      AccountType
	Currency  string

	OwnerKind AccountOwnerKind
	OwnerID   *string

	AllowNegative bool
	Status        AccountStatus

	CreatedAt time.Time
	UpdatedAt time.Time
}

// JournalKind classifies why money moved, for reporting and for finding the
// journals of one sort when something goes wrong.
type JournalKind string

const (
	JournalTopup      JournalKind = "topup"
	JournalPurchase   JournalKind = "purchase"
	JournalRefund     JournalKind = "refund"
	JournalTransfer   JournalKind = "transfer"
	JournalGrant      JournalKind = "grant"
	JournalFee        JournalKind = "fee"
	JournalSettlement JournalKind = "settlement"
	JournalAdjustment JournalKind = "adjustment"
	JournalReversal   JournalKind = "reversal"
	JournalExpiry     JournalKind = "expiry"
)

// Journal is one balanced transaction. It is never updated: corrections are new
// journals pointing back at the original.
type Journal struct {
	ID       int64
	PublicID string

	// ExternalID is what makes posting idempotent, e.g. payment:pay_123:capture.
	ExternalID string

	Kind      JournalKind
	ProductID *int64

	SourceKind *string
	SourceID   *string

	ReversesJournalID *int64

	Memo     string
	PostedAt time.Time

	Postings []*Posting

	CreatedAt time.Time
}

// Posting is one leg of a journal. Immutable once written.
type Posting struct {
	ID        int64
	JournalID int64
	AccountID int64

	// AccountCode is set when reading, to save callers a join.
	AccountCode string

	Direction Direction
	// Amount is always positive; Direction carries the sign.
	Amount   int64
	Currency string
	Seq      int

	// BalanceAfter is the account's raw balance immediately after this posting.
	BalanceAfter int64

	CreatedAt time.Time
}

// Signed is what this posting contributes to its account's raw balance.
func (p *Posting) Signed() int64 {
	return int64(p.Direction) * p.Amount
}

// Balance is an account's materialised position.
type Balance struct {
	AccountID  int64
	RawBalance int64
	Held       int64
	Version    int64
	UpdatedAt  time.Time
}

// Natural converts the raw balance into the direction the account reads in.
func (b *Balance) Natural(accountType AccountType) int64 {
	return b.RawBalance * accountType.NormalSign()
}

// Available is what can still be spent: the natural balance less what holds
// have reserved.
func (b *Balance) Available(accountType AccountType) int64 {
	return b.Natural(accountType) - b.Held
}
