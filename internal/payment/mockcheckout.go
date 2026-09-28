package payment

import (
	"bytes"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"

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
func MockCheckoutHandler(prefix, webhookPath string, m *mock.Provider, webhooks http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, prefix)
		if refundID, ok := strings.CutPrefix(id, "refunds/"); ok {
			mockRefund(w, r, refundID, webhookPath, m, webhooks)
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
