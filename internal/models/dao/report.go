package dao

import "time"

// PnLLine is one income or expense account's movement over a period, in the
// account's natural direction (income earned, expense incurred).
type PnLLine struct {
	ProductID       int64
	ProductPublicID string
	ProductCode     string
	Currency        string
	AccountCode     string
	Type            AccountType
	Amount          int64
}

// ProviderStats counts one provider's payment attempts started in a period,
// by where they are now.
type ProviderStats struct {
	Provider       string
	Attempts       int64
	Captured       int64
	Failed         int64
	Expired        int64
	Cancelled      int64
	Open           int64
	CapturedAmount int64
	TopFailures    []FailureCount
}

type FailureCount struct {
	Code  string
	Count int64
}

// OpsSnapshot is the operational state alerts watch: things that should be
// zero, or close to it, when the background jobs are keeping up.
type OpsSnapshot struct {
	// StuckPayments are open past the point the expiry job should have
	// closed them.
	StuckPayments int64
	// EventBacklog is stored webhooks not yet processed; OldestEvent is when
	// the oldest of them arrived (nil when there are none).
	EventBacklog int64
	OldestEvent  *time.Time
	// OverdueHolds are active holds past their expiry that the sweeper has
	// not released: customer money locked for nothing.
	OverdueHolds int64
}
