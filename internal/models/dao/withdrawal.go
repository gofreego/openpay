package dao

import "time"

type BeneficiaryStatus string

const (
	BeneficiaryPending  BeneficiaryStatus = "pending"
	BeneficiaryVerified BeneficiaryStatus = "verified"
	BeneficiaryFailed   BeneficiaryStatus = "failed"
)

// Beneficiary is where a customer's withdrawals may go.
type Beneficiary struct {
	ID         int64
	PublicID   string
	CustomerID int64
	Kind       string // bank_account or vpa
	Name       string
	// AccountNumber is sealed with pkg/fieldcrypt in the database; the
	// engines open it only to send a payout.
	AccountNumber      *string
	AccountLast4       *string
	AccountFingerprint *string
	IFSC               *string
	VPA                *string
	Status             BeneficiaryStatus
	NameAtBank         *string
	VerifiedAt         *time.Time
	FailureReason      *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type WithdrawalStatus string

const (
	WithdrawalPendingApproval WithdrawalStatus = "pending_approval"
	WithdrawalApproved        WithdrawalStatus = "approved"
	WithdrawalProcessing      WithdrawalStatus = "processing"
	WithdrawalPaid            WithdrawalStatus = "paid"
	WithdrawalFailed          WithdrawalStatus = "failed"
	WithdrawalRejected        WithdrawalStatus = "rejected"
	WithdrawalReversed        WithdrawalStatus = "reversed"
)

type Withdrawal struct {
	ID            int64
	PublicID      string
	WalletID      int64
	CustomerID    int64
	ProductID     *int64
	BeneficiaryID int64

	Amount   int64
	Currency string

	Status           WithdrawalStatus
	RequiresApproval bool
	RequestedBy      string
	DecidedBy        *string
	DecidedAt        *time.Time
	DecisionNote     *string

	Provider         string
	ProviderPayoutID *string
	FailureReason    *string
	PaidAt           *time.Time

	WalletPublicID      string
	BeneficiaryPublicID string

	CreatedAt time.Time
	UpdatedAt time.Time
}
