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

// Event is a webhook after its signature has been verified. It is only a hint
// that something changed: the engine fetches the payment before acting.
type Event struct {
	EventID           string
	Type              string
	ProviderPaymentID string
	OccurredAt        time.Time
}

type Capabilities struct {
	// ManualCapture: the provider can authorize without capturing, leaving
	// OpenPay to capture explicitly.
	ManualCapture bool
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
