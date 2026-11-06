package razorpay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// fakeRazorpay keeps payment links and refunds in memory, as Razorpay would.
type fakeRazorpay struct {
	mu      sync.Mutex
	links   map[string]map[string]any
	byRef   map[string]string
	refunds map[string]map[string]any
}

func newFake(t *testing.T) (*Provider, *fakeRazorpay) {
	f := &fakeRazorpay{links: map[string]map[string]any{}, byRef: map[string]string{}, refunds: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p, err := New(Config{KeyID: "rzp_test", KeySecret: "s3cret", WebhookSecret: "whsec", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return p, f
}

func (f *fakeRazorpay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if user, pass, _ := r.BasicAuth(); user != "rzp_test" || pass != "s3cret" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body map[string]any
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	bad := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		reply(map[string]any{"error": map[string]any{"code": "BAD_REQUEST_ERROR", "description": msg}})
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/payment_links":
		ref := body["reference_id"].(string)
		if _, taken := f.byRef[ref]; taken {
			bad("reference_id already exists")
			return
		}
		id := "plink_" + ref
		f.byRef[ref] = id
		f.links[id] = map[string]any{
			"id": id, "status": "created", "amount": body["amount"], "amount_paid": 0,
			"currency": body["currency"], "short_url": "https://rzp.io/i/" + ref, "reference_id": ref,
		}
		reply(f.links[id])
	case r.Method == http.MethodGet && r.URL.Path == "/v1/payment_links":
		var found []any
		if id, ok := f.byRef[r.URL.Query().Get("reference_id")]; ok {
			found = append(found, f.links[id])
		}
		reply(map[string]any{"payment_links": found})
	case r.Method == http.MethodGet && len(parts) == 3 && parts[1] == "payment_links":
		link, ok := f.links[parts[2]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reply(link)
	case r.Method == http.MethodPost && len(parts) == 4 && parts[3] == "cancel":
		link := f.links[parts[2]]
		if link["status"] != "created" {
			bad("cannot cancel")
			return
		}
		link["status"] = "cancelled"
		reply(link)
	case r.Method == http.MethodGet && len(parts) == 4 && parts[3] == "refunds":
		var items []any
		for _, rf := range f.refunds {
			if rf["payment_id"] == parts[2] {
				items = append(items, rf)
			}
		}
		reply(map[string]any{"items": items})
	case r.Method == http.MethodPost && len(parts) == 4 && parts[3] == "refund":
		id := "rfnd_" + body["receipt"].(string)
		f.refunds[id] = map[string]any{
			"id": id, "payment_id": parts[2], "amount": body["amount"], "currency": "INR",
			"status": "pending", "receipt": body["receipt"], "notes": body["notes"],
		}
		reply(f.refunds[id])
	case r.Method == http.MethodGet && len(parts) == 3 && parts[1] == "refunds":
		reply(f.refunds[parts[2]])
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// pay marks a link paid with one captured payment, as the customer would.
func (f *fakeRazorpay) pay(linkID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	link := f.links[linkID]
	link["status"] = "paid"
	link["amount_paid"] = link["amount"]
	link["payments"] = []any{map[string]any{"payment_id": "pay_1", "amount": link["amount"], "status": "captured"}}
}

func TestPaymentLinkLifecycle(t *testing.T) {
	p, f := newFake(t)
	ctx := context.Background()
	req := provider.CreateRequest{AttemptID: "att1", Amount: 19900, Currency: "INR", Description: "Premium", ReturnURL: "https://zshala.com/premium/return"}

	created, err := p.CreatePayment(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if created.ProviderPaymentID != "plink_att1" || created.Checkout.URL != "https://rzp.io/i/att1" {
		t.Fatalf("created %+v", created)
	}
	// A retried create finds the same link.
	again, err := p.CreatePayment(ctx, req)
	if err != nil || again.ProviderPaymentID != created.ProviderPaymentID {
		t.Fatalf("retry: %+v %v", again, err)
	}

	pay, err := p.FetchPayment(ctx, created.ProviderPaymentID)
	if err != nil || pay.Status != provider.StatusPending || pay.Amount != 19900 {
		t.Fatalf("pending: %+v %v", pay, err)
	}
	f.pay(created.ProviderPaymentID)
	pay, err = p.FetchPayment(ctx, created.ProviderPaymentID)
	if err != nil || pay.Status != provider.StatusCaptured || pay.Amount != 19900 {
		t.Fatalf("paid: %+v %v", pay, err)
	}
	if err := p.Cancel(ctx, created.ProviderPaymentID); !apperrors.Is(err, apperrors.FailedPrecondition) {
		t.Fatalf("cancelling a paid link: %v", err)
	}

	// A refund goes to the payment on the link; asking again returns it.
	refund, err := p.Refund(ctx, provider.RefundRequest{RefundID: "r1", ProviderPaymentID: created.ProviderPaymentID, Amount: 19900, Currency: "INR"})
	if err != nil || refund.ProviderRefundID != "rfnd_r1" || refund.ProviderPaymentID != created.ProviderPaymentID || refund.Status != provider.RefundPending {
		t.Fatalf("refund: %+v %v", refund, err)
	}
	if again, err := p.Refund(ctx, provider.RefundRequest{RefundID: "r1", ProviderPaymentID: created.ProviderPaymentID, Amount: 19900, Currency: "INR"}); err != nil || again.ProviderRefundID != "rfnd_r1" || len(f.refunds) != 1 {
		t.Fatalf("refund retry: %+v %v (%d refunds)", again, err, len(f.refunds))
	}
	fetched, err := p.FetchRefund(ctx, "rfnd_r1")
	if err != nil || fetched.ProviderPaymentID != created.ProviderPaymentID {
		t.Fatalf("fetch refund: %+v %v", fetched, err)
	}
}

func TestCancelUnpaidLink(t *testing.T) {
	p, _ := newFake(t)
	ctx := context.Background()
	created, err := p.CreatePayment(ctx, provider.CreateRequest{AttemptID: "att2", Amount: 100, Currency: "INR"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Cancel(ctx, created.ProviderPaymentID); err != nil {
		t.Fatal(err)
	}
	if err := p.Cancel(ctx, created.ProviderPaymentID); err != nil {
		t.Fatalf("cancelling twice: %v", err)
	}
	pay, _ := p.FetchPayment(ctx, created.ProviderPaymentID)
	if pay.Status != provider.StatusCancelled {
		t.Fatalf("status %s", pay.Status)
	}
}

func TestWebhooks(t *testing.T) {
	p, _ := newFake(t)
	sign := func(body string) http.Header {
		mac := hmac.New(sha256.New, []byte("whsec"))
		mac.Write([]byte(body))
		h := http.Header{}
		h.Set(SignatureHeader, hex.EncodeToString(mac.Sum(nil)))
		h.Set(EventIDHeader, "evt_1")
		return h
	}

	paid := `{"event":"payment_link.paid","created_at":1700000000,"payload":{"payment_link":{"entity":{"id":"plink_att1"}}}}`
	ev, err := p.VerifyWebhook(sign(paid), []byte(paid))
	if err != nil || ev.ObjectKind != provider.ObjectPayment || ev.ObjectID != "plink_att1" || ev.EventID != "evt_1" {
		t.Fatalf("paid event: %+v %v", ev, err)
	}

	refunded := `{"event":"refund.processed","created_at":1,"payload":{"refund":{"entity":{"id":"rfnd_r1"}}}}`
	if ev, err := p.VerifyWebhook(sign(refunded), []byte(refunded)); err != nil || ev.ObjectKind != provider.ObjectRefund || ev.ObjectID != "rfnd_r1" {
		t.Fatalf("refund event: %+v %v", ev, err)
	}

	other := `{"event":"order.paid","created_at":1,"payload":{}}`
	if ev, err := p.VerifyWebhook(sign(other), []byte(other)); err != nil || ev.ObjectID != "" {
		t.Fatalf("other event should be kept but not acted on: %+v %v", ev, err)
	}

	if _, err := p.VerifyWebhook(sign(paid), []byte(strings.Replace(paid, "att1", "att9", 1))); !apperrors.Is(err, apperrors.Unauthenticated) {
		t.Fatalf("tampered body: %v", err)
	}
}
