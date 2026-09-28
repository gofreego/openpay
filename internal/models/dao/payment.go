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
	// OrderID is set for an order payment.
	OrderID *int64

	Amount   int64
	Currency string

	Status      PaymentStatus
	Application PaymentApplication

	Provider       *string
	CapturedAmount *int64
	CapturedAt     *time.Time
	// SettledAt is when the provider paid this payment out to our bank.
	SettledAt *time.Time
	// RefundedAmount is what refunds have promised back so far — initiated,
	// pending or processed. The database refuses it beyond CapturedAmount.
	RefundedAmount int64

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
	ID            int64
	Provider      string
	EventID       string
	EventType     string
	ObjectKind    string
	ObjectID      *string
	Payload       []byte
	ReceivedAt    time.Time
	ProcessedAt   *time.Time
	Attempts      int
	NextAttemptAt time.Time
	LastError     *string
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

const (
	PaymentPartiallyRefunded PaymentStatus = "partially_refunded"
	PaymentRefunded          PaymentStatus = "refunded"
)

type RefundStatus string

const (
	// RefundInitiated: money reserved; the provider has not yet confirmed it
	// holds the request. A timeout leaves a refund here and the poller asks again.
	RefundInitiated RefundStatus = "initiated"
	RefundPending   RefundStatus = "pending"
	RefundProcessed RefundStatus = "processed"
	RefundFailed    RefundStatus = "failed"
)

func (s RefundStatus) IsFinal() bool { return s == RefundProcessed || s == RefundFailed }

// RefundSource is where a refund's money was taken from.
type RefundSource string

const (
	// RefundFromWallet: the customer's wallet, which the top-up credited.
	RefundFromWallet RefundSource = "wallet"
	// RefundFromUnapplied: the refunds_payable balance of a payment its
	// wallet refused.
	RefundFromUnapplied RefundSource = "unapplied"
	// RefundFromOrder: an order refund already moved the card share into
	// refunds_payable, with its own tax split.
	RefundFromOrder RefundSource = "order"
)

type Refund struct {
	ID        int64
	PublicID  string
	PaymentID int64
	ProductID int64

	Amount   int64
	Currency string
	Status   RefundStatus
	Source   RefundSource

	ReasonCode string
	Memo       string

	Provider         string
	ProviderRefundID *string
	FailureCode      *string
	FailureReason    *string

	RequestedBy string
	ProcessedAt *time.Time

	// PaymentPublicID is read alongside, for the API.
	PaymentPublicID string

	CreatedAt time.Time
	UpdatedAt time.Time
}

const (
	PaymentDisputed    PaymentStatus = "disputed"
	PaymentDisputeWon  PaymentStatus = "dispute_won"
	PaymentDisputeLost PaymentStatus = "dispute_lost"
)

type DisputeStatus string

const (
	DisputeOpen        DisputeStatus = "open"
	DisputeUnderReview DisputeStatus = "under_review"
	DisputeWon         DisputeStatus = "won"
	DisputeLost        DisputeStatus = "lost"
)

func (s DisputeStatus) IsResolved() bool { return s == DisputeWon || s == DisputeLost }

// Dispute is a chargeback: a customer's bank taking a payment back.
type Dispute struct {
	ID                int64
	PublicID          string
	PaymentID         int64
	ProductID         int64
	Provider          string
	ProviderDisputeID string
	Amount            int64
	Currency          string
	Reason            string
	Status            DisputeStatus

	// Where the contested money came from when the dispute opened; a win puts
	// each part back. They sum to Amount.
	FromWallet    int64
	FromUnapplied int64
	FromExpense   int64

	EvidenceDueBy       *time.Time
	Evidence            *string
	EvidenceSubmittedBy *string
	EvidenceSubmittedAt *time.Time
	ResolvedAt          *time.Time

	PaymentPublicID string

	CreatedAt time.Time
	UpdatedAt time.Time
}
