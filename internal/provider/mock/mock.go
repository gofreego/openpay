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
	"strings"
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
	refunded  int64
	settled   bool
}

// Provider is the mock PSP.
type Provider struct {
	name        string
	down        bool
	secret      []byte
	checkoutURL string

	mu       sync.Mutex
	payments map[string]*payment
	// byAttempt makes CreatePayment idempotent on our attempt id, as a real
	// provider's idempotency key would.
	byAttempt map[string]string

	disputes map[string]*provider.Dispute

	payouts map[string]*provider.Payout
	// byPayoutID makes CreatePayout idempotent on our payout id.
	byPayoutID     map[string]string
	failNextPayout error

	settlements []*provider.Settlement
	// settledRefs are refunds and disputes already in a settlement.
	settledRefs map[string]bool

	refunds map[string]*provider.Refund
	// byRefundID makes Refund idempotent on our refund id.
	byRefundID map[string]string

	// Programmable behaviour for the next calls.
	failNextCreate error
	failNextRefund error
	manualCapture  bool
}

// New returns a mock signing webhooks with secret. checkoutURL is the base of
// the hosted page a customer is sent to; the payment id is appended.
func New(secret, checkoutURL string) *Provider {
	return NewNamed(Name, secret, checkoutURL)
}

// NewNamed is a mock under another name — a second provider for testing
// failover between two.
func NewNamed(name, secret, checkoutURL string) *Provider {
	return &Provider{
		name:        name,
		secret:      []byte(secret),
		checkoutURL: checkoutURL,
		payments:    map[string]*payment{},
		byAttempt:   map[string]string{},
		refunds:     map[string]*provider.Refund{},
		disputes:    map[string]*provider.Dispute{},
		settledRefs: map[string]bool{},
		payouts:     map[string]*provider.Payout{},
		byPayoutID:  map[string]string{},
		byRefundID:  map[string]string{},
	}
}

func (m *Provider) Name() string { return m.name }

// SetUnavailable makes every create and fetch fail as an outage would, until
// switched back.
func (m *Provider) SetUnavailable(down bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.down = down
}

func (m *Provider) outage() error {
	if m.down {
		return provider.ErrUnavailable("%s: service unavailable", m.name)
	}
	return nil
}

func (m *Provider) Capabilities() provider.Capabilities {
	m.mu.Lock()
	defer m.mu.Unlock()
	return provider.Capabilities{ManualCapture: m.manualCapture}
}

func (m *Provider) CreatePayment(_ context.Context, req provider.CreateRequest) (*provider.CreateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.outage(); err != nil {
		return nil, err
	}
	if err := m.failNextCreate; err != nil {
		m.failNextCreate = nil
		return nil, err
	}
	id, ok := m.byAttempt[req.AttemptID]
	if !ok {
		id = m.name + "pay_" + ids.New(ids.Payment)[4:]
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
	if err := m.outage(); err != nil {
		return nil, err
	}
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

// Refund is asynchronous, as real refunds are: it answers pending, and
// ProcessRefund or FailRefund decide how it ends. It refuses to refund more
// than was captured and not yet refunded, as a real provider would.
func (m *Provider) Refund(_ context.Context, req provider.RefundRequest) (*provider.Refund, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.failNextRefund; err != nil {
		m.failNextRefund = nil
		return nil, err
	}
	if id, ok := m.byRefundID[req.RefundID]; ok {
		out := *m.refunds[id]
		return &out, nil
	}
	p, ok := m.payments[req.ProviderPaymentID]
	if !ok {
		return nil, apperrors.New(apperrors.NotFound, "mock payment %q not found", req.ProviderPaymentID)
	}
	if p.Status != provider.StatusCaptured {
		return nil, apperrors.New(apperrors.FailedPrecondition, "mock payment %q is %s, not captured", p.ProviderPaymentID, p.Status)
	}
	if p.refunded+req.Amount > p.Amount {
		return nil, apperrors.New(apperrors.FailedPrecondition,
			"mock payment %q has %d refundable, asked for %d", p.ProviderPaymentID, p.Amount-p.refunded, req.Amount)
	}

	p.refunded += req.Amount
	id := "mockrfnd_" + ids.New(ids.Refund)[4:]
	r := &provider.Refund{ProviderRefundID: id, ProviderPaymentID: p.ProviderPaymentID,
		Status: provider.RefundPending, Amount: req.Amount, Currency: req.Currency}
	m.refunds[id] = r
	m.byRefundID[req.RefundID] = id
	out := *r
	return &out, nil
}

func (m *Provider) FetchRefund(_ context.Context, providerRefundID string) (*provider.Refund, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.refunds[providerRefundID]
	if !ok {
		return nil, apperrors.New(apperrors.NotFound, "mock refund %q not found", providerRefundID)
	}
	out := *r
	return &out, nil
}

// webhook is the mock's wire format.
type webhook struct {
	EventID    string    `json:"event_id"`
	Type       string    `json:"type"`
	Object     string    `json:"object"`
	ObjectID   string    `json:"object_id"`
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
	return &provider.Event{EventID: w.EventID, Type: w.Type, ObjectKind: provider.ObjectKind(w.Object),
		ObjectID: w.ObjectID, OccurredAt: w.OccurredAt}, nil
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

// ProcessRefund completes a refund: the money has left.
func (m *Provider) ProcessRefund(providerRefundID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.refunds[providerRefundID]; ok && r.Status == provider.RefundPending {
		r.Status = provider.RefundProcessed
	}
}

// FailRefund fails a refund, returning its amount to what can be refunded.
func (m *Provider) FailRefund(providerRefundID, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.refunds[providerRefundID]; ok && r.Status == provider.RefundPending {
		r.Status = provider.RefundFailed
		r.FailureReason = reason
		m.payments[r.ProviderPaymentID].refunded -= r.Amount
	}
}

// FailNextRefund makes the next Refund return err, e.g. a timeout.
func (m *Provider) FailNextRefund(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNextRefund = err
}

// Webhook builds a signed webhook for a payment, as the provider would send
// it. Deliver it as many times, as late, and in whatever order a test needs.
func (m *Provider) Webhook(providerPaymentID, eventType string) (http.Header, []byte) {
	return m.webhookFor(provider.ObjectPayment, providerPaymentID, eventType)
}

// RefundWebhook builds a signed webhook about a refund.
func (m *Provider) RefundWebhook(providerRefundID, eventType string) (http.Header, []byte) {
	return m.webhookFor(provider.ObjectRefund, providerRefundID, eventType)
}

func (m *Provider) webhookFor(kind provider.ObjectKind, objectID, eventType string) (http.Header, []byte) {
	body, _ := json.Marshal(webhook{
		EventID: "mockevt_" + ids.New(ids.OutboxEvent)[4:], Type: eventType,
		Object: string(kind), ObjectID: objectID, OccurredAt: time.Now().UTC(),
	})
	headers := http.Header{}
	headers.Set(SignatureHeader, hex.EncodeToString(m.sign(body)))
	return headers, body
}

func (m *Provider) FetchDispute(_ context.Context, providerDisputeID string) (*provider.Dispute, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.disputes[providerDisputeID]
	if !ok {
		return nil, apperrors.New(apperrors.NotFound, "mock dispute %q not found", providerDisputeID)
	}
	out := *d
	return &out, nil
}

func (m *Provider) SubmitDisputeEvidence(_ context.Context, providerDisputeID, evidence string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.disputes[providerDisputeID]
	if !ok {
		return apperrors.New(apperrors.NotFound, "mock dispute %q not found", providerDisputeID)
	}
	if d.Status != provider.DisputeOpen {
		return apperrors.New(apperrors.FailedPrecondition, "mock dispute %q is %s, not open", providerDisputeID, d.Status)
	}
	d.Status = provider.DisputeUnderReview
	return nil
}

// OpenDispute is the customer's bank charging a payment back.
func (m *Provider) OpenDispute(providerPaymentID string, amount int64, reason string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := "mockdsp_" + ids.New(ids.Dispute)[4:]
	due := time.Now().Add(7 * 24 * time.Hour).UTC()
	m.disputes[id] = &provider.Dispute{ProviderDisputeID: id, ProviderPaymentID: providerPaymentID,
		Status: provider.DisputeOpen, Amount: amount, Currency: "INR", Reason: reason, EvidenceDueBy: &due}
	return id
}

// ResolveDispute is the network's decision.
func (m *Provider) ResolveDispute(providerDisputeID string, won bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.disputes[providerDisputeID]; ok {
		d.Status = provider.DisputeLost
		if won {
			d.Status = provider.DisputeWon
		}
	}
}

// DisputeWebhook builds a signed webhook about a dispute.
func (m *Provider) DisputeWebhook(providerDisputeID, eventType string) (http.Header, []byte) {
	return m.webhookFor(provider.ObjectDispute, providerDisputeID, eventType)
}

func (m *Provider) FetchSettlements(_ context.Context, since time.Time) ([]*provider.Settlement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*provider.Settlement
	for _, st := range m.settlements {
		if st.SettledAt.After(since) {
			copied := *st
			copied.Items = append([]provider.SettlementItem(nil), st.Items...)
			out = append(out, &copied)
		}
	}
	return out, nil
}

// Settle pays out everything not yet settled — captured payments, processed
// refunds, lost chargebacks — as one settlement, charging feeBps on payments
// plus 18% GST on that fee. tamper, if given, edits the settlement before it
// is published: the way a test injects a mismatch the reconciliation must
// catch. It returns the settlement id.
func (m *Provider) Settle(feeBps int64, tamper func(*provider.Settlement)) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	st := &provider.Settlement{
		ProviderSettlementID: "mockstl_" + ids.New(ids.Settlement)[4:],
		SettledAt:            time.Now().UTC(), BankReference: "UTR" + ids.New(ids.Settlement)[4:16],
		Currency: "INR",
	}
	for _, p := range m.payments {
		if p.Status != provider.StatusCaptured || p.settled {
			continue
		}
		p.settled = true
		fee := p.Amount * feeBps / 10000
		tax := fee * 18 / 100
		st.Items = append(st.Items, provider.SettlementItem{Kind: provider.SettlePayment, ProviderRef: p.ProviderPaymentID,
			Gross: p.Amount, Fee: fee, FeeTax: tax, Net: p.Amount - fee - tax})
	}
	for id, r := range m.refunds {
		if r.Status != provider.RefundProcessed || m.settledRefs[id] {
			continue
		}
		m.settledRefs[id] = true
		st.Items = append(st.Items, provider.SettlementItem{Kind: provider.SettleRefund, ProviderRef: id,
			Gross: -r.Amount, Net: -r.Amount})
	}
	for id, d := range m.disputes {
		if d.Status != provider.DisputeLost || m.settledRefs[id] {
			continue
		}
		m.settledRefs[id] = true
		st.Items = append(st.Items, provider.SettlementItem{Kind: provider.SettleChargeback, ProviderRef: id,
			Gross: -d.Amount, Net: -d.Amount})
	}
	if tamper != nil {
		tamper(st)
	}
	st.Raw, _ = json.Marshal(st.Items)
	m.settlements = append(m.settlements, st)
	return st.ProviderSettlementID
}

// VerifyDestination verifies everything except bank accounts starting 0000
// and VPAs starting "invalid", which fail — the way tests reach the unhappy path.
func (m *Provider) VerifyDestination(_ context.Context, d provider.Destination) (*provider.Verification, error) {
	if strings.HasPrefix(d.AccountNumber, "0000") || strings.HasPrefix(d.VPA, "invalid") {
		return &provider.Verification{FailureReason: "mock: account does not exist"}, nil
	}
	return &provider.Verification{Verified: true, NameAtBank: strings.ToUpper(d.Name)}, nil
}

// CreatePayout accepts a payout as processing; CompletePayout, FailPayout and
// ReversePayout decide how it ends.
func (m *Provider) CreatePayout(_ context.Context, req provider.PayoutRequest) (*provider.Payout, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failNextPayout; err != nil {
		m.failNextPayout = nil
		return nil, err
	}
	if id, ok := m.byPayoutID[req.PayoutID]; ok {
		out := *m.payouts[id]
		return &out, nil
	}
	id := "mockpout_" + ids.New(ids.Payout)[4:]
	m.payouts[id] = &provider.Payout{ProviderPayoutID: id, Status: provider.PayoutProcessing, Amount: req.Amount}
	m.byPayoutID[req.PayoutID] = id
	out := *m.payouts[id]
	return &out, nil
}

func (m *Provider) FetchPayout(_ context.Context, providerPayoutID string) (*provider.Payout, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.payouts[providerPayoutID]
	if !ok {
		return nil, apperrors.New(apperrors.NotFound, "mock payout %q not found", providerPayoutID)
	}
	out := *p
	return &out, nil
}

func (m *Provider) setPayout(id string, from, to provider.PayoutStatus, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.payouts[id]; ok && p.Status == from {
		p.Status, p.FailureReason = to, reason
	}
}

// CompletePayout: the money reached the bank.
func (m *Provider) CompletePayout(id string) {
	m.setPayout(id, provider.PayoutProcessing, provider.PayoutPaid, "")
}

// FailPayout: the payout never left.
func (m *Provider) FailPayout(id, reason string) {
	m.setPayout(id, provider.PayoutProcessing, provider.PayoutFailed, reason)
}

// ReversePayout: it was paid, then the receiving bank sent it back.
func (m *Provider) ReversePayout(id, reason string) {
	m.setPayout(id, provider.PayoutPaid, provider.PayoutReversed, reason)
}

// FailNextPayout makes the next CreatePayout return err, e.g. a timeout.
func (m *Provider) FailNextPayout(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNextPayout = err
}

// PayoutWebhook builds a signed webhook about a payout.
func (m *Provider) PayoutWebhook(providerPayoutID, eventType string) (http.Header, []byte) {
	return m.webhookFor(provider.ObjectPayout, providerPayoutID, eventType)
}

// PayoutCount is how many payouts the mock has been asked to make.
func (m *Provider) PayoutCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.payouts)
}
