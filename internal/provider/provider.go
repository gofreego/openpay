// Package provider is the boundary between OpenPay and the PSPs that actually
// move money. Everything provider-specific stays behind Provider; the payment
// engine sees only canonical statuses and normalized events.
package provider

import (
	"context"
	"net/http"
	"time"

	"github.com/gofreego/openpay/pkg/apperrors"
)

// Status is a provider payment's state in OpenPay's vocabulary. Each provider
// maps its own statuses onto these, so retries, UX and reconciliation never
// branch on which provider took the payment.
type Status string

const (
	StatusCreated    Status = "created"
	StatusPending    Status = "pending"
	StatusAuthorized Status = "authorized"
	StatusCaptured   Status = "captured"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
)

// CreateRequest asks a provider to start collecting a payment.
type CreateRequest struct {
	// AttemptID is our id for this try. Providers that support it use it as
	// their idempotency key, so a retried create cannot open two orders.
	AttemptID string
	Amount    int64
	Currency  string
	// Description appears on the customer's statement; it carries the
	// product name so they recognise the charge (plan.md phase 5).
	Description string
	ReturnURL   string
}

// Checkout is what the customer's client needs to pay: a hosted page to send
// them to. Card data never touches OpenPay (plan.md D9).
type Checkout struct {
	URL string
}

type CreateResult struct {
	ProviderPaymentID string
	Checkout          Checkout
}

// Payment is the provider's authoritative view of a payment. Amounts come
// from here, never from the client (plan.md D7).
type Payment struct {
	ProviderPaymentID string
	Status            Status
	// Amount is what the provider says was authorized or captured.
	Amount   int64
	Currency string

	// FailureCode is canonical: declined, insufficient_funds, risk or
	// technical. FailureReason is the provider's own words.
	FailureCode   string
	FailureReason string
}

// ObjectKind says what a webhook is about.
type ObjectKind string

const (
	ObjectPayment ObjectKind = "payment"
	ObjectRefund  ObjectKind = "refund"
	ObjectDispute ObjectKind = "dispute"
)

// Event is a webhook after its signature has been verified. It is only a hint
// that something changed: the engine fetches the object before acting.
type Event struct {
	EventID    string
	Type       string
	ObjectKind ObjectKind
	// ObjectID is the provider's id for the payment, refund or dispute.
	ObjectID   string
	OccurredAt time.Time
}

// RefundStatus is a provider refund's state in OpenPay's vocabulary.
type RefundStatus string

const (
	RefundPending   RefundStatus = "pending"
	RefundProcessed RefundStatus = "processed"
	RefundFailed    RefundStatus = "failed"
)

type RefundRequest struct {
	// RefundID is our id. Providers use it as the idempotency key, so asking
	// again after a timeout returns the same refund rather than a second one.
	RefundID          string
	ProviderPaymentID string
	Amount            int64
	Currency          string
}

// Refund is the provider's authoritative view of a refund.
type Refund struct {
	ProviderRefundID  string
	ProviderPaymentID string
	Status            RefundStatus
	Amount            int64
	Currency          string
	FailureReason     string
}

type Capabilities struct {
	// ManualCapture: the provider can authorize without capturing, leaving
	// OpenPay to capture explicitly.
	ManualCapture bool
}

// DisputeStatus is a provider dispute's state in OpenPay's vocabulary.
type DisputeStatus string

const (
	DisputeOpen        DisputeStatus = "open"
	DisputeUnderReview DisputeStatus = "under_review"
	DisputeWon         DisputeStatus = "won"
	DisputeLost        DisputeStatus = "lost"
)

// Dispute is the provider's authoritative view of a chargeback.
type Dispute struct {
	ProviderDisputeID string
	ProviderPaymentID string
	Status            DisputeStatus
	Amount            int64
	Currency          string
	Reason            string
	EvidenceDueBy     *time.Time
}

// SettlementItemKind is what a settlement line settles.
type SettlementItemKind string

const (
	SettlePayment    SettlementItemKind = "payment"
	SettleRefund     SettlementItemKind = "refund"
	SettleChargeback SettlementItemKind = "chargeback"
)

// SettlementItem is one line of a settlement report. Amounts are signed as
// the provider reports them: money in positive, refunds and chargebacks
// negative. Net is what the line contributed to the bank credit.
type SettlementItem struct {
	Kind SettlementItemKind
	// ProviderRef is the provider's id for the payment, refund or dispute.
	ProviderRef string
	Gross       int64
	Fee         int64
	// FeeTax is GST the provider charged on its fee: reclaimable input tax,
	// not a payment cost (plan.md D13).
	FeeTax int64
	Net    int64
}

// Settlement is one payout from the provider to our bank: many payments,
// from every product, netted into one credit.
type Settlement struct {
	ProviderSettlementID string
	SettledAt            time.Time
	BankReference        string
	Currency             string
	Items                []SettlementItem
	// Raw is the report as the provider sent it, kept as evidence.
	Raw []byte
}

// Provider is one PSP. Implementations must be safe for concurrent use, and
// every mutating call must be idempotent at the provider (their own
// idempotency keys) so a retry after a timeout cannot move money twice.
type Provider interface {
	Name() string
	Capabilities() Capabilities

	CreatePayment(ctx context.Context, req CreateRequest) (*CreateResult, error)
	FetchPayment(ctx context.Context, providerPaymentID string) (*Payment, error)
	Capture(ctx context.Context, providerPaymentID string, amount int64) error
	Cancel(ctx context.Context, providerPaymentID string) error

	// Refund asks the provider to return money. Refunds are rarely
	// synchronous: the usual answer is pending, and a webhook or a fetch says
	// how it ended.
	Refund(ctx context.Context, req RefundRequest) (*Refund, error)
	FetchRefund(ctx context.Context, providerRefundID string) (*Refund, error)

	// Disputes are opened by the customer's bank, never by us: we learn of
	// them by webhook, fetch them, and answer with evidence.
	FetchDispute(ctx context.Context, providerDisputeID string) (*Dispute, error)
	SubmitDisputeEvidence(ctx context.Context, providerDisputeID, evidence string) error

	// FetchSettlements returns the settlements made after since. Real
	// providers serve these by API or as SFTP/CSV reports; either way they
	// arrive here normalized.
	FetchSettlements(ctx context.Context, since time.Time) ([]*Settlement, error)

	// VerifyWebhook authenticates a webhook and normalizes it. An invalid
	// signature is an Unauthenticated error; nothing from such a request may
	// be stored or acted on.
	VerifyWebhook(headers http.Header, body []byte) (*Event, error)
}

// ErrUnavailable marks a failure where the provider's state is unknown — a
// timeout, a 5xx. The caller must not assume the call did nothing.
func ErrUnavailable(format string, args ...any) error {
	return apperrors.New(apperrors.Unavailable, format, args...)
}

// Registry holds the configured providers and the order to prefer them in.
type Registry struct {
	providers map[string]Provider
	priority  []string
}

func NewRegistry(priority []string, providers ...Provider) *Registry {
	r := &Registry{providers: map[string]Provider{}, priority: priority}
	for _, p := range providers {
		r.providers[p.Name()] = p
	}
	return r
}

// Names lists the configured providers, in priority order where given.
func (r *Registry) Names() []string {
	var names []string
	for _, name := range r.priority {
		if _, ok := r.providers[name]; ok {
			names = append(names, name)
		}
	}
	return names
}

// Get returns a provider by name.
func (r *Registry) Get(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, apperrors.New(apperrors.NotFound, "payment provider %q is not configured", name)
	}
	return p, nil
}

// Choose picks the provider for a new attempt, and says why. A priority list,
// not a rules engine (plan.md phase 5): health-based failover and method
// rules extend this when there are two real providers to choose between.
func (r *Registry) Choose() (Provider, string, error) {
	for _, name := range r.priority {
		if p, ok := r.providers[name]; ok {
			return p, "priority: first configured provider", nil
		}
	}
	return nil, "", apperrors.New(apperrors.Unavailable, "no payment provider is configured")
}
