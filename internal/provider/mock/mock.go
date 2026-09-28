// Package mock is a payment provider OpenPay controls completely.
//
// It is a first-class implementation, not test scaffolding (plan.md phase 5):
// it runs in CI and in local development, and its job is to be hostile on
// demand — decline, time out, report a different amount, and deliver
// webhooks late, twice, out of order, or not at all. If the payment engine
// survives this provider, the failures real providers produce are covered.
//
// State lives in memory, so it serves one process: dev runs every app in one
// process, and tests drive it directly.
package mock

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

// Name is how the mock is configured and how its webhooks are routed.
const Name = "mock"

// SignatureHeader carries the webhook signature: hex HMAC-SHA256 of the body.
const SignatureHeader = "X-Mock-Signature"

type payment struct {
	provider.Payment
	attemptID string
}

// Provider is the mock PSP.
type Provider struct {
	secret      []byte
	checkoutURL string

	mu       sync.Mutex
	payments map[string]*payment
	// byAttempt makes CreatePayment idempotent on our attempt id, as a real
	// provider's idempotency key would.
	byAttempt map[string]string

	// Programmable behaviour for the next calls.
	failNextCreate error
	manualCapture  bool
}

// New returns a mock signing webhooks with secret. checkoutURL is the base of
// the hosted page a customer is sent to; the payment id is appended.
func New(secret, checkoutURL string) *Provider {
	return &Provider{
		secret:      []byte(secret),
		checkoutURL: checkoutURL,
		payments:    map[string]*payment{},
		byAttempt:   map[string]string{},
	}
}

func (m *Provider) Name() string { return Name }

func (m *Provider) Capabilities() provider.Capabilities {
	m.mu.Lock()
	defer m.mu.Unlock()
	return provider.Capabilities{ManualCapture: m.manualCapture}
}

func (m *Provider) CreatePayment(_ context.Context, req provider.CreateRequest) (*provider.CreateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.failNextCreate; err != nil {
		m.failNextCreate = nil
		return nil, err
	}
	id, ok := m.byAttempt[req.AttemptID]
	if !ok {
		id = "mockpay_" + ids.New(ids.Payment)[4:]
		m.payments[id] = &payment{
			Payment: provider.Payment{
				ProviderPaymentID: id, Status: provider.StatusPending,
				Amount: req.Amount, Currency: req.Currency,
			},
			attemptID: req.AttemptID,
		}
		m.byAttempt[req.AttemptID] = id
	}
	return &provider.CreateResult{
		ProviderPaymentID: id,
		Checkout:          provider.Checkout{URL: m.checkoutURL + id},
	}, nil
}

func (m *Provider) FetchPayment(_ context.Context, providerPaymentID string) (*provider.Payment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.payments[providerPaymentID]
	if !ok {
		return nil, apperrors.New(apperrors.NotFound, "mock payment %q not found", providerPaymentID)
	}
	out := p.Payment
	return &out, nil
}

func (m *Provider) Capture(_ context.Context, providerPaymentID string, amount int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.payments[providerPaymentID]
	if !ok {
		return apperrors.New(apperrors.NotFound, "mock payment %q not found", providerPaymentID)
	}
	switch p.Status {
	case provider.StatusCaptured:
		return nil // idempotent, as a real capture is
	case provider.StatusAuthorized:
		p.Status = provider.StatusCaptured
		p.Amount = amount
		return nil
	default:
		return apperrors.New(apperrors.FailedPrecondition, "mock payment %q is %s, not authorized", providerPaymentID, p.Status)
	}
}

func (m *Provider) Cancel(_ context.Context, providerPaymentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.payments[providerPaymentID]
	if !ok {
		return apperrors.New(apperrors.NotFound, "mock payment %q not found", providerPaymentID)
	}
	switch p.Status {
	case provider.StatusCreated, provider.StatusPending, provider.StatusAuthorized:
		p.Status = provider.StatusCancelled
		return nil
	case provider.StatusCancelled:
		return nil
	default:
		return apperrors.New(apperrors.FailedPrecondition, "mock payment %q is %s and cannot be cancelled", providerPaymentID, p.Status)
	}
}

// webhook is the mock's wire format.
type webhook struct {
	EventID    string    `json:"event_id"`
	Type       string    `json:"type"`
	PaymentID  string    `json:"payment_id"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (m *Provider) VerifyWebhook(headers http.Header, body []byte) (*provider.Event, error) {
	got, err := hex.DecodeString(headers.Get(SignatureHeader))
	if err != nil || !hmac.Equal(got, m.sign(body)) {
		return nil, apperrors.New(apperrors.Unauthenticated, "webhook signature is invalid")
	}
	var w webhook
	if err := json.Unmarshal(body, &w); err != nil || w.EventID == "" {
		return nil, apperrors.New(apperrors.InvalidArgument, "webhook body is not a mock event")
	}
	return &provider.Event{EventID: w.EventID, Type: w.Type, ProviderPaymentID: w.PaymentID, OccurredAt: w.OccurredAt}, nil
}

func (m *Provider) sign(body []byte) []byte {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write(body)
	return mac.Sum(nil)
}

// ---- Controls: what a customer, the network, or a misbehaving PSP would do ----

// Pay completes the customer's side: captured, or only authorized when manual
// capture is on.
func (m *Provider) Pay(providerPaymentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.payments[providerPaymentID]; ok {
		p.Status = provider.StatusCaptured
		if m.manualCapture {
			p.Status = provider.StatusAuthorized
		}
	}
}

// Decline fails the payment with a canonical failure code.
func (m *Provider) Decline(providerPaymentID, code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.payments[providerPaymentID]; ok {
		p.Status = provider.StatusFailed
		p.FailureCode = code
		p.FailureReason = "mock: declined by issuer"
	}
}

// ReportAmount makes the provider claim a different amount than was asked
// for — the mismatch D7 exists to catch.
func (m *Provider) ReportAmount(providerPaymentID string, amount int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.payments[providerPaymentID]; ok {
		p.Amount = amount
	}
}

// FailNextCreate makes the next CreatePayment return err, e.g. a timeout
// (provider.ErrUnavailable) or a decline.
func (m *Provider) FailNextCreate(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNextCreate = err
}

// SetManualCapture makes Pay stop at authorized.
func (m *Provider) SetManualCapture(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.manualCapture = on
}

// Webhook builds a signed webhook for a payment, as the provider would send
// it. Deliver it as many times, as late, and in whatever order a test needs.
func (m *Provider) Webhook(providerPaymentID, eventType string) (http.Header, []byte) {
	body, _ := json.Marshal(webhook{
		EventID: "mockevt_" + ids.New(ids.OutboxEvent)[4:], Type: eventType,
		PaymentID: providerPaymentID, OccurredAt: time.Now().UTC(),
	})
	headers := http.Header{}
	headers.Set(SignatureHeader, hex.EncodeToString(m.sign(body)))
	return headers, body
}
