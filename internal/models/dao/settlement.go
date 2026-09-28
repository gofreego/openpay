package dao

import "time"

type SettlementStatus string

const (
	SettlementClean  SettlementStatus = "clean"
	SettlementBreaks SettlementStatus = "breaks"
)

type Settlement struct {
	ID                   int64
	PublicID             string
	Provider             string
	ProviderSettlementID string
	Bank                 string
	BankReference        string
	SettledAt            time.Time

	Gross, Fees, FeeTax, Net int64
	Currency                 string
	ItemCount                int
	Raw                      []byte
	Status                   SettlementStatus
	CreatedAt                time.Time
}

// Classification is how a settlement line compared with the ledger.
type Classification string

const (
	Matched           Classification = "matched"
	MissingInLedger   Classification = "missing_in_ledger"
	MissingAtProvider Classification = "missing_at_provider"
	AmountMismatch    Classification = "amount_mismatch"
	FeeMismatch       Classification = "fee_mismatch"
	Duplicate         Classification = "duplicate"
)

type SettlementItem struct {
	ID           int64
	SettlementID int64
	Kind         string
	ProviderRef  string

	Gross, Fee, FeeTax, Net int64
	// Expected is what the ledger expected to settle; Unexplained is the rest
	// (Net + Fee + FeeTax - Expected), which goes to suspense.
	Expected    int64
	Unexplained int64

	PaymentID *int64
	RefundID  *int64
	DisputeID *int64
	ProductID *int64

	Classification Classification
}

type BreakStatus string

const (
	BreakOpen         BreakStatus = "open"
	BreakResolved     BreakStatus = "resolved"
	BreakForceMatched BreakStatus = "force_matched"
	BreakWrittenOff   BreakStatus = "written_off"
)

// ReconBreak is an item of the reconciliation work queue.
type ReconBreak struct {
	ID               int64
	PublicID         string
	Provider         string
	Classification   Classification
	SettlementItemID *int64
	PaymentID        *int64
	ProductID        *int64
	Amount           int64
	Currency         string
	Detail           string

	Status     BreakStatus
	ReasonCode *string
	Note       *string
	ResolvedBy *string
	ResolvedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}
