package payment

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/provider"

	"github.com/gofreego/goutils/logger"
)

// RequestLogger records provider calls.
type RequestLogger interface {
	RecordProviderRequest(ctx context.Context, req *dao.ProviderRequest) error
}

// logged wraps a provider so every outbound call lands in the request log —
// the evidence for "what did we ask Razorpay, and what did it say?". Only the
// request and result structs are logged, and they carry no credentials:
// those live inside the provider implementation and never cross this line.
type logged struct {
	provider.Provider
	log RequestLogger
}

func withRequestLog(p provider.Provider, log RequestLogger) provider.Provider {
	return &logged{Provider: p, log: log}
}

func (l *logged) record(ctx context.Context, operation, ref string, req, resp any, err error, started time.Time) {
	entry := &dao.ProviderRequest{
		Provider:   l.Name(),
		Operation:  operation,
		PaymentID:  &ref,
		DurationMs: int(time.Since(started).Milliseconds()),
	}
	if req != nil {
		entry.Request, _ = json.Marshal(req)
	}
	if resp != nil && err == nil {
		entry.Response, _ = json.Marshal(resp)
	}
	if err != nil {
		msg := err.Error()
		entry.Error = &msg
	}
	// Logging must never fail a payment; a missing log line is a lesser loss.
	if logErr := l.log.RecordProviderRequest(context.WithoutCancel(ctx), entry); logErr != nil {
		logger.Warn(ctx, "failed to log %s %s call: %v", l.Name(), operation, logErr)
	}
}

func (l *logged) CreatePayment(ctx context.Context, req provider.CreateRequest) (*provider.CreateResult, error) {
	started := time.Now()
	res, err := l.Provider.CreatePayment(ctx, req)
	l.record(ctx, "create_payment", req.AttemptID, req, res, err, started)
	return res, err
}

func (l *logged) FetchPayment(ctx context.Context, id string) (*provider.Payment, error) {
	started := time.Now()
	res, err := l.Provider.FetchPayment(ctx, id)
	l.record(ctx, "fetch_payment", id, nil, res, err, started)
	return res, err
}

func (l *logged) Capture(ctx context.Context, id string, amount int64) error {
	started := time.Now()
	err := l.Provider.Capture(ctx, id, amount)
	l.record(ctx, "capture", id, map[string]int64{"amount": amount}, nil, err, started)
	return err
}

func (l *logged) Cancel(ctx context.Context, id string) error {
	started := time.Now()
	err := l.Provider.Cancel(ctx, id)
	l.record(ctx, "cancel", id, nil, nil, err, started)
	return err
}

func (l *logged) Refund(ctx context.Context, req provider.RefundRequest) (*provider.Refund, error) {
	started := time.Now()
	res, err := l.Provider.Refund(ctx, req)
	l.record(ctx, "refund", req.RefundID, req, res, err, started)
	return res, err
}

func (l *logged) FetchRefund(ctx context.Context, id string) (*provider.Refund, error) {
	started := time.Now()
	res, err := l.Provider.FetchRefund(ctx, id)
	l.record(ctx, "fetch_refund", id, nil, res, err, started)
	return res, err
}

func (l *logged) FetchDispute(ctx context.Context, id string) (*provider.Dispute, error) {
	started := time.Now()
	res, err := l.Provider.FetchDispute(ctx, id)
	l.record(ctx, "fetch_dispute", id, nil, res, err, started)
	return res, err
}

func (l *logged) SubmitDisputeEvidence(ctx context.Context, id, evidence string) error {
	started := time.Now()
	err := l.Provider.SubmitDisputeEvidence(ctx, id, evidence)
	// The evidence itself may carry customer details; the dispute row keeps
	// it, the request log only its size.
	l.record(ctx, "submit_dispute_evidence", id, map[string]int{"evidence_bytes": len(evidence)}, nil, err, started)
	return err
}

func (l *logged) FetchSettlements(ctx context.Context, since time.Time) ([]*provider.Settlement, error) {
	started := time.Now()
	res, err := l.Provider.FetchSettlements(ctx, since)
	l.record(ctx, "fetch_settlements", since.Format(time.RFC3339), nil, map[string]int{"settlements": len(res)}, err, started)
	return res, err
}

func (l *logged) VerifyDestination(ctx context.Context, d provider.Destination) (*provider.Verification, error) {
	started := time.Now()
	res, err := l.Provider.VerifyDestination(ctx, d)
	// The account number is not logged: only whether one was checked.
	l.record(ctx, "verify_destination", "", map[string]bool{"bank_account": d.AccountNumber != "", "vpa": d.VPA != ""}, res, err, started)
	return res, err
}

func (l *logged) CreatePayout(ctx context.Context, req provider.PayoutRequest) (*provider.Payout, error) {
	started := time.Now()
	res, err := l.Provider.CreatePayout(ctx, req)
	l.record(ctx, "create_payout", req.PayoutID, map[string]any{"amount": req.Amount, "currency": req.Currency}, res, err, started)
	return res, err
}

func (l *logged) FetchPayout(ctx context.Context, id string) (*provider.Payout, error) {
	started := time.Now()
	res, err := l.Provider.FetchPayout(ctx, id)
	l.record(ctx, "fetch_payout", id, nil, res, err, started)
	return res, err
}

// VerifyWebhook is inbound, not a call we made; the raw event is kept in
// provider_events instead.
func (l *logged) VerifyWebhook(headers http.Header, body []byte) (*provider.Event, error) {
	return l.Provider.VerifyWebhook(headers, body)
}
