// Package razorpay is the Razorpay provider. Customers pay on a Razorpay
// Payment Link (Razorpay's hosted page: cards, UPI, netbanking, wallets), so
// card data never touches OpenPay; Razorpay captures automatically.
//
// The provider payment id is the payment link's id (plink_…). Refunds go to
// the payment made on it (pay_…), found from the link.
//
// Not built yet: settlements (reconciliation reads none from Razorpay),
// dispute evidence (answered in the Razorpay dashboard) and payouts (those
// need RazorpayX).
package razorpay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/pkg/apperrors"
)

const (
	Name           = "razorpay"
	defaultBaseURL = "https://api.razorpay.com"

	SignatureHeader = "X-Razorpay-Signature"
	EventIDHeader   = "X-Razorpay-Event-Id"
)

// Config is the Razorpay account's API key and webhook secret. KeySecret and
// WebhookSecret are secrets: keep them in a secret store.
type Config struct {
	Enabled       bool   `yaml:"Enabled"`
	KeyID         string `yaml:"KeyID"`
	KeySecret     string `yaml:"KeySecret"`
	WebhookSecret string `yaml:"WebhookSecret"`
	// BaseURL is Razorpay's API; empty is production (test keys use it too).
	BaseURL string `yaml:"BaseURL"`
}

type Provider struct {
	cfg  Config
	http *http.Client
}

// New makes the provider. Timeouts and retries come from the resilient
// wrapper around it.
func New(cfg Config) (*Provider, error) {
	if cfg.KeyID == "" || cfg.KeySecret == "" || cfg.WebhookSecret == "" {
		return nil, errors.New("razorpay: KeyID, KeySecret and WebhookSecret are required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	return &Provider{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (p *Provider) Name() string { return Name }

// Payment Links capture automatically: there is no separate capture.
func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{ManualCapture: false}
}

// apiError is Razorpay's error body.
type apiError struct {
	Error struct {
		Code        string `json:"code"`
		Description string `json:"description"`
		Reason      string `json:"reason"`
	} `json:"error"`
}

// call sends a request and decodes the JSON answer into out. A 5xx or a
// network failure is Unavailable: the state at Razorpay is unknown.
func (p *Provider) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return apperrors.New(apperrors.Internal, "razorpay: encoding request: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.cfg.BaseURL+path, reader)
	if err != nil {
		return apperrors.New(apperrors.Internal, "razorpay: %v", err)
	}
	req.SetBasicAuth(p.cfg.KeyID, p.cfg.KeySecret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return provider.ErrUnavailable("razorpay %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return provider.ErrUnavailable("razorpay %s %s: reading answer: %v", method, path, err)
	}
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return provider.ErrUnavailable("razorpay %s %s: %d", method, path, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		var e apiError
		_ = json.Unmarshal(raw, &e)
		code := apperrors.InvalidArgument
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			code = apperrors.Unauthenticated
		case http.StatusNotFound:
			code = apperrors.NotFound
		}
		return apperrors.New(code, "razorpay %s %s: %s", method, path, e.Error.Description)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return apperrors.New(apperrors.Internal, "razorpay %s %s: decoding answer: %v", method, path, err)
	}
	return nil
}

type paymentLink struct {
	ID          string `json:"id"`
	Status      string `json:"status"` // created, partially_paid, paid, expired, cancelled
	Amount      int64  `json:"amount"`
	AmountPaid  int64  `json:"amount_paid"`
	Currency    string `json:"currency"`
	ShortURL    string `json:"short_url"`
	ReferenceID string `json:"reference_id"`
	Payments    []struct {
		PaymentID string `json:"payment_id"`
		Amount    int64  `json:"amount"`
		Status    string `json:"status"`
	} `json:"payments"`
}

// CreatePayment opens a Payment Link. AttemptID is its reference_id, which
// Razorpay keeps unique: a retried create finds the link it already made.
func (p *Provider) CreatePayment(ctx context.Context, req provider.CreateRequest) (*provider.CreateResult, error) {
	body := map[string]any{
		"amount":          req.Amount,
		"currency":        req.Currency,
		"accept_partial":  false,
		"description":     truncate(req.Description, 2048),
		"reference_id":    req.AttemptID,
		"reminder_enable": false,
		"notes":           map[string]string{"attempt_id": req.AttemptID},
	}
	if req.ReturnURL != "" {
		body["callback_url"] = req.ReturnURL
		body["callback_method"] = "get"
	}
	var link paymentLink
	err := p.call(ctx, http.MethodPost, "/v1/payment_links", body, &link)
	if apperrors.Is(err, apperrors.InvalidArgument) {
		// Most likely made already (reference_id taken): use that one.
		if existing, findErr := p.linkByReference(ctx, req.AttemptID); findErr == nil && existing != nil {
			link, err = *existing, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return &provider.CreateResult{ProviderPaymentID: link.ID, Checkout: provider.Checkout{URL: link.ShortURL}}, nil
}

func (p *Provider) linkByReference(ctx context.Context, reference string) (*paymentLink, error) {
	var list struct {
		PaymentLinks []paymentLink `json:"payment_links"`
	}
	if err := p.call(ctx, http.MethodGet, "/v1/payment_links?reference_id="+url.QueryEscape(reference), nil, &list); err != nil {
		return nil, err
	}
	for _, l := range list.PaymentLinks {
		if l.ReferenceID == reference {
			return &l, nil
		}
	}
	return nil, nil
}

func (p *Provider) link(ctx context.Context, linkID string) (*paymentLink, error) {
	var link paymentLink
	if err := p.call(ctx, http.MethodGet, "/v1/payment_links/"+url.PathEscape(linkID), nil, &link); err != nil {
		return nil, err
	}
	return &link, nil
}

func (p *Provider) FetchPayment(ctx context.Context, providerPaymentID string) (*provider.Payment, error) {
	link, err := p.link(ctx, providerPaymentID)
	if err != nil {
		return nil, err
	}
	out := &provider.Payment{ProviderPaymentID: link.ID, Currency: link.Currency, Amount: link.AmountPaid}
	switch link.Status {
	case "paid":
		out.Status = provider.StatusCaptured
	case "cancelled":
		out.Status = provider.StatusCancelled
	case "expired":
		out.Status = provider.StatusFailed
		out.FailureCode = "technical"
		out.FailureReason = "the payment link expired"
	default: // created, partially_paid (partial payments are off)
		out.Status = provider.StatusPending
		out.Amount = link.Amount
	}
	return out, nil
}

// Capture is never needed: Payment Links capture on their own.
func (p *Provider) Capture(context.Context, string, int64) error { return nil }

func (p *Provider) Cancel(ctx context.Context, providerPaymentID string) error {
	err := p.call(ctx, http.MethodPost, "/v1/payment_links/"+url.PathEscape(providerPaymentID)+"/cancel", map[string]any{}, nil)
	if apperrors.Is(err, apperrors.InvalidArgument) {
		// Paid, expired or cancelled already: say how it stands.
		if pay, fetchErr := p.FetchPayment(ctx, providerPaymentID); fetchErr == nil {
			switch pay.Status {
			case provider.StatusCancelled, provider.StatusFailed:
				return nil
			case provider.StatusCaptured:
				return apperrors.New(apperrors.FailedPrecondition, "razorpay link %s is paid and cannot be cancelled", providerPaymentID)
			}
		}
	}
	return err
}

type refund struct {
	ID        string            `json:"id"`
	PaymentID string            `json:"payment_id"`
	Amount    int64             `json:"amount"`
	Currency  string            `json:"currency"`
	Status    string            `json:"status"` // pending, processed, failed
	Receipt   string            `json:"receipt"`
	Notes     map[string]string `json:"notes"`
}

func (r *refund) toProvider() *provider.Refund {
	out := &provider.Refund{
		ProviderRefundID:  r.ID,
		ProviderPaymentID: r.Notes["payment_link_id"],
		Amount:            r.Amount,
		Currency:          r.Currency,
		Status:            provider.RefundPending,
	}
	switch r.Status {
	case "processed":
		out.Status = provider.RefundProcessed
	case "failed":
		out.Status = provider.RefundFailed
		out.FailureReason = "razorpay could not process the refund"
	}
	return out
}

// Refund returns money from the payment made on the link. RefundID is the
// refund's receipt: asking again returns the refund already made.
func (p *Provider) Refund(ctx context.Context, req provider.RefundRequest) (*provider.Refund, error) {
	link, err := p.link(ctx, req.ProviderPaymentID)
	if err != nil {
		return nil, err
	}
	paymentID := ""
	for _, pay := range link.Payments {
		if pay.Status == "captured" {
			paymentID = pay.PaymentID
		}
	}
	if paymentID == "" {
		return nil, apperrors.New(apperrors.FailedPrecondition, "razorpay link %s has no captured payment to refund", req.ProviderPaymentID)
	}

	var existing struct {
		Items []refund `json:"items"`
	}
	if err := p.call(ctx, http.MethodGet, "/v1/payments/"+url.PathEscape(paymentID)+"/refunds?count=100", nil, &existing); err != nil {
		return nil, err
	}
	for _, r := range existing.Items {
		if r.Receipt == req.RefundID {
			r.Notes = withLink(r.Notes, req.ProviderPaymentID)
			return r.toProvider(), nil
		}
	}

	var made refund
	if err := p.call(ctx, http.MethodPost, "/v1/payments/"+url.PathEscape(paymentID)+"/refund", map[string]any{
		"amount":  req.Amount,
		"speed":   "normal",
		"receipt": req.RefundID,
		"notes":   map[string]string{"payment_link_id": req.ProviderPaymentID, "refund_id": req.RefundID},
	}, &made); err != nil {
		return nil, err
	}
	made.Notes = withLink(made.Notes, req.ProviderPaymentID)
	return made.toProvider(), nil
}

func withLink(notes map[string]string, linkID string) map[string]string {
	if notes == nil {
		notes = map[string]string{}
	}
	if notes["payment_link_id"] == "" {
		notes["payment_link_id"] = linkID
	}
	return notes
}

func (p *Provider) FetchRefund(ctx context.Context, providerRefundID string) (*provider.Refund, error) {
	var r refund
	if err := p.call(ctx, http.MethodGet, "/v1/refunds/"+url.PathEscape(providerRefundID), nil, &r); err != nil {
		return nil, err
	}
	return r.toProvider(), nil
}

func (p *Provider) FetchDispute(ctx context.Context, providerDisputeID string) (*provider.Dispute, error) {
	var d struct {
		ID         string `json:"id"`
		PaymentID  string `json:"payment_id"`
		Amount     int64  `json:"amount"`
		Currency   string `json:"currency"`
		Status     string `json:"status"` // open, under_review, won, lost, closed
		ReasonCode string `json:"reason_code"`
		RespondBy  int64  `json:"respond_by"`
	}
	if err := p.call(ctx, http.MethodGet, "/v1/disputes/"+url.PathEscape(providerDisputeID), nil, &d); err != nil {
		return nil, err
	}
	out := &provider.Dispute{
		ProviderDisputeID: d.ID,
		ProviderPaymentID: d.PaymentID,
		Amount:            d.Amount,
		Currency:          d.Currency,
		Reason:            d.ReasonCode,
		Status:            provider.DisputeOpen,
	}
	switch d.Status {
	case "under_review":
		out.Status = provider.DisputeUnderReview
	case "won":
		out.Status = provider.DisputeWon
	case "lost", "closed":
		out.Status = provider.DisputeLost
	}
	if d.RespondBy > 0 {
		t := time.Unix(d.RespondBy, 0)
		out.EvidenceDueBy = &t
	}
	return out, nil
}

func (p *Provider) SubmitDisputeEvidence(context.Context, string, string) error {
	return apperrors.New(apperrors.FailedPrecondition, "razorpay: answer disputes in the Razorpay dashboard")
}

// FetchSettlements reads none yet: Razorpay settlements are not reconciled.
func (p *Provider) FetchSettlements(context.Context, time.Time) ([]*provider.Settlement, error) {
	return nil, nil
}

var errNoPayouts = apperrors.New(apperrors.FailedPrecondition, "razorpay: payouts need RazorpayX, which is not set up")

func (p *Provider) VerifyDestination(context.Context, provider.Destination) (*provider.Verification, error) {
	return nil, errNoPayouts
}

func (p *Provider) CreatePayout(context.Context, provider.PayoutRequest) (*provider.Payout, error) {
	return nil, errNoPayouts
}

func (p *Provider) FetchPayout(context.Context, string) (*provider.Payout, error) {
	return nil, errNoPayouts
}

// VerifyWebhook checks X-Razorpay-Signature (HMAC-SHA256 of the body with the
// webhook secret) and says which object the event is about. Events OpenPay
// doesn't act on come back with no object, and are stored and ignored.
func (p *Provider) VerifyWebhook(headers http.Header, body []byte) (*provider.Event, error) {
	got, err := hex.DecodeString(headers.Get(SignatureHeader))
	mac := hmac.New(sha256.New, []byte(p.cfg.WebhookSecret))
	mac.Write(body)
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		return nil, apperrors.New(apperrors.Unauthenticated, "webhook signature is invalid")
	}
	var w struct {
		Event     string `json:"event"`
		CreatedAt int64  `json:"created_at"`
		Payload   struct {
			PaymentLink struct {
				Entity struct {
					ID string `json:"id"`
				} `json:"entity"`
			} `json:"payment_link"`
			Refund struct {
				Entity struct {
					ID string `json:"id"`
				} `json:"entity"`
			} `json:"refund"`
			Dispute struct {
				Entity struct {
					ID string `json:"id"`
				} `json:"entity"`
			} `json:"dispute"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &w); err != nil || w.Event == "" {
		return nil, apperrors.New(apperrors.InvalidArgument, "webhook body is not a razorpay event")
	}
	event := &provider.Event{
		EventID:    headers.Get(EventIDHeader),
		Type:       w.Event,
		OccurredAt: time.Unix(w.CreatedAt, 0),
	}
	switch {
	case strings.HasPrefix(w.Event, "payment_link."):
		event.ObjectKind, event.ObjectID = provider.ObjectPayment, w.Payload.PaymentLink.Entity.ID
	case strings.HasPrefix(w.Event, "refund."):
		event.ObjectKind, event.ObjectID = provider.ObjectRefund, w.Payload.Refund.Entity.ID
	case strings.HasPrefix(w.Event, "payment.dispute."):
		event.ObjectKind, event.ObjectID = provider.ObjectDispute, w.Payload.Dispute.Entity.ID
	}
	if event.EventID == "" {
		sum := sha256.Sum256(body)
		event.EventID = hex.EncodeToString(sum[:16])
	}
	return event, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var _ provider.Provider = (*Provider)(nil)

// String hides the secrets from logs.
func (c Config) String() string {
	return fmt.Sprintf("razorpay{enabled=%v key=%s}", c.Enabled, c.KeyID)
}
