package payment

import (
	"bytes"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/internal/provider/mock"
)

// MockCheckoutHandler is the mock provider's hosted checkout page, for local
// development only. It stands in for the page a real PSP would host: the
// customer pays or declines there, and the mock then sends its signed webhook
// through the real webhook handler — so the whole pipeline runs end to end
// without a PSP account.
//
// Routes, under prefix + the provider's payment id:
//   - GET shows the page.
//   - POST ?outcome=pay pays and sends the webhook.
//   - POST ?outcome=decline declines and sends the webhook.
//   - Adding &webhook=false changes state but sends no webhook, to watch the
//     poller recover a lost one.
//   - POST refunds/{providerRefundID}?outcome=process|fail finishes a refund
//     the same way.
//   - POST disputes/open/{providerPaymentID}?amount=N charges a payment back;
//     POST disputes/{providerDisputeID}?outcome=won|lost decides it.
//   - POST settle?fee_bps=N pays out everything unsettled as one settlement;
//     &inflate=M adds M to the first line, to watch a break be caught.
//   - POST payouts/{providerPayoutID}?outcome=paid|fail|reverse finishes a
//     withdrawal's payout — reverse bounces one already paid.
func MockCheckoutHandler(prefix, webhookPath string, m *mock.Provider, webhooks http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, prefix)
		if refundID, ok := strings.CutPrefix(id, "refunds/"); ok {
			mockRefund(w, r, refundID, webhookPath, m, webhooks)
			return
		}
		if payoutID, ok := strings.CutPrefix(id, "payouts/"); ok {
			mockPayout(w, r, payoutID, webhookPath, m, webhooks)
			return
		}
		if id == "settle" {
			mockSettle(w, r, m)
			return
		}
		if rest, ok := strings.CutPrefix(id, "disputes/"); ok {
			mockDispute(w, r, rest, webhookPath, m, webhooks)
			return
		}
		p, err := m.FetchPayment(r.Context(), id)
		if err != nil {
			http.Error(w, "unknown mock payment", http.StatusNotFound)
			return
		}

		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Mock checkout</title>
<body style="font-family:system-ui;max-width:28rem;margin:4rem auto">
<h1>Mock checkout</h1><p>%s — ₹%d.%02d (%s)</p>
<form method="post" action="?outcome=pay"><button>Pay</button></form>
<form method="post" action="?outcome=decline"><button>Decline</button></form>
<p><small>Local development only. No money moves.</small></p>`,
				html.EscapeString(id), p.Amount/100, p.Amount%100, html.EscapeString(string(p.Status)))
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		eventType := "payment.captured"
		switch r.URL.Query().Get("outcome") {
		case "pay":
			m.Pay(id)
		case "decline":
			m.Decline(id, "declined")
			eventType = "payment.failed"
		default:
			http.Error(w, "outcome must be pay or decline", http.StatusBadRequest)
			return
		}

		delivered := "not sent"
		if r.URL.Query().Get("webhook") != "false" {
			headers, body := m.Webhook(id, eventType)
			req := httptest.NewRequest(http.MethodPost, webhookPath, bytes.NewReader(body)).WithContext(r.Context())
			req.Header = headers
			rec := httptest.NewRecorder()
			webhooks.ServeHTTP(rec, req)
			delivered = fmt.Sprintf("delivered, answered %d", rec.Code)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"payment":%q,"event":%q,"webhook":%q}`, id, eventType, delivered)
	})
}

func mockRefund(w http.ResponseWriter, r *http.Request, id, webhookPath string, m *mock.Provider, webhooks http.Handler) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, err := m.FetchRefund(r.Context(), id); err != nil {
		http.Error(w, "unknown mock refund", http.StatusNotFound)
		return
	}
	eventType := "refund.processed"
	switch r.URL.Query().Get("outcome") {
	case "process":
		m.ProcessRefund(id)
	case "fail":
		m.FailRefund(id, "mock: refund failed")
		eventType = "refund.failed"
	default:
		http.Error(w, "outcome must be process or fail", http.StatusBadRequest)
		return
	}
	headers, body := m.RefundWebhook(id, eventType)
	req := httptest.NewRequest(http.MethodPost, webhookPath, bytes.NewReader(body)).WithContext(r.Context())
	req.Header = headers
	rec := httptest.NewRecorder()
	webhooks.ServeHTTP(rec, req)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"refund":%q,"event":%q,"webhook":"delivered, answered %d"}`, id, eventType, rec.Code)
}

func mockDispute(w http.ResponseWriter, r *http.Request, rest, webhookPath string, m *mock.Provider, webhooks http.Handler) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var disputeID, eventType string
	if paymentID, ok := strings.CutPrefix(rest, "open/"); ok {
		p, err := m.FetchPayment(r.Context(), paymentID)
		if err != nil {
			http.Error(w, "unknown mock payment", http.StatusNotFound)
			return
		}
		amount := p.Amount
		if a, err := strconv.ParseInt(r.URL.Query().Get("amount"), 10, 64); err == nil && a > 0 {
			amount = a
		}
		disputeID, eventType = m.OpenDispute(paymentID, amount, "fraud"), "dispute.created"
	} else {
		if _, err := m.FetchDispute(r.Context(), rest); err != nil {
			http.Error(w, "unknown mock dispute", http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("outcome") {
		case "won":
			m.ResolveDispute(rest, true)
		case "lost":
			m.ResolveDispute(rest, false)
		default:
			http.Error(w, "outcome must be won or lost", http.StatusBadRequest)
			return
		}
		disputeID, eventType = rest, "dispute.closed"
	}
	headers, body := m.DisputeWebhook(disputeID, eventType)
	req := httptest.NewRequest(http.MethodPost, webhookPath, bytes.NewReader(body)).WithContext(r.Context())
	req.Header = headers
	rec := httptest.NewRecorder()
	webhooks.ServeHTTP(rec, req)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"dispute":%q,"event":%q,"webhook":"delivered, answered %d"}`, disputeID, eventType, rec.Code)
}

func mockSettle(w http.ResponseWriter, r *http.Request, m *mock.Provider) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	feeBps, _ := strconv.ParseInt(r.URL.Query().Get("fee_bps"), 10, 64)
	inflate, _ := strconv.ParseInt(r.URL.Query().Get("inflate"), 10, 64)
	lines := 0
	id := m.Settle(feeBps, func(st *provider.Settlement) {
		lines = len(st.Items)
		if inflate != 0 && lines > 0 {
			st.Items[0].Gross += inflate
			st.Items[0].Net += inflate
		}
	})
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"settlement":%q,"lines":%d}`, id, lines)
}

func mockPayout(w http.ResponseWriter, r *http.Request, id, webhookPath string, m *mock.Provider, webhooks http.Handler) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, err := m.FetchPayout(r.Context(), id); err != nil {
		http.Error(w, "unknown mock payout", http.StatusNotFound)
		return
	}
	switch r.URL.Query().Get("outcome") {
	case "paid":
		m.CompletePayout(id)
	case "fail":
		m.FailPayout(id, "mock: payout failed")
	case "reverse":
		m.ReversePayout(id, "mock: beneficiary account closed")
	default:
		http.Error(w, "outcome must be paid, fail or reverse", http.StatusBadRequest)
		return
	}
	headers, body := m.PayoutWebhook(id, "payout.updated")
	req := httptest.NewRequest(http.MethodPost, webhookPath, bytes.NewReader(body)).WithContext(r.Context())
	req.Header = headers
	rec := httptest.NewRecorder()
	webhooks.ServeHTTP(rec, req)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"payout":%q,"webhook":"delivered, answered %d"}`, id, rec.Code)
}
