package dao

import "time"

type PaymentStatus string

const (
	PaymentCreated    PaymentStatus = "created"
	PaymentPending    PaymentStatus = "pending"
	PaymentAuthorized PaymentStatus = "authorized"
	PaymentCaptured   PaymentStatus = "captured"
	PaymentSettled    PaymentStatus = "settled"
	PaymentFailed     PaymentStatus = "failed"
	PaymentExpired    PaymentStatus = "expired"
	PaymentCancelled  PaymentStatus = "cancelled"
)

// IsOpen reports whether the payment may still collect money.
func (s PaymentStatus) IsOpen() bool {
	return s == PaymentCreated || s == PaymentPending || s == PaymentAuthorized
}

type PaymentPurpose string

const (
	PurposeWalletTopup PaymentPurpose = "wallet_topup"
	PurposeOrder       PaymentPurpose = "order"
)

// PaymentApplication is where captured money went in the ledger.
type PaymentApplication string

const (
	ApplicationPending PaymentApplication = "pending"
	// ApplicationApplied: credited to its purpose.
	ApplicationApplied PaymentApplication = "applied"
	// ApplicationUnapplied: the purpose refused it; owed back to the customer.
	ApplicationUnapplied PaymentApplication = "unapplied"
	// ApplicationSuspense: the provider's figures disagreed with ours.
	ApplicationSuspense PaymentApplication = "suspense"
)

type Payment struct {
	ID         int64
	PublicID   string
	ProductID  int64
	CustomerID *int64
	Purpose    PaymentPurpose
	WalletID   *int64

	Amount   int64
	Currency string

	Status      PaymentStatus
	Application PaymentApplication

	Provider       *string
	CapturedAmount *int64
	CapturedAt     *time.Time

	FailureCode   *string
	FailureReason *string

	Description string
	ReturnURL   string
	ExpiresAt   time.Time

	// Public ids, read alongside for the API.
	ProductPublicID  string
	CustomerPublicID *string
	WalletPublicID   *string

	CreatedAt time.Time
	UpdatedAt time.Time
}

type PaymentAttempt struct {
	ID                int64
	PublicID          string
	PaymentID         int64
	Provider          string
	RoutingReason     string
	ProviderPaymentID *string
	Status            string
	CheckoutURL       *string
	FailureCode       *string
	FailureReason     *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// PaymentTransition is one state change and what caused it.
type PaymentTransition struct {
	ID        int64
	PaymentID int64
	From      PaymentStatus
	To        PaymentStatus
	// Source is api, webhook, poller, sweeper or operator.
	Source    string
	Reference *string
	Detail    *string
	CreatedAt time.Time
}

// ProviderEvent is a webhook exactly as received, plus its processing state.
type ProviderEvent struct {
	ID                int64
	Provider          string
	EventID           string
	EventType         string
	ProviderPaymentID *string
	Payload           []byte
	ReceivedAt        time.Time
	ProcessedAt       *time.Time
	Attempts          int
	NextAttemptAt     time.Time
	LastError         *string
}

// ProviderRequest is one call to a provider, for the request log.
type ProviderRequest struct {
	Provider   string
	Operation  string
	PaymentID  *string
	Request    []byte
	Response   []byte
	Error      *string
	DurationMs int
}
