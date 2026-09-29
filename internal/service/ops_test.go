package service_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/opsmetrics"
	"github.com/gofreego/openpay/internal/testsupport"
)

// Each operational gauge counts exactly the condition it alerts on, and
// nothing that merely looks similar: a fresh open payment is not stuck, a
// processed webhook is not backlog, a hold not yet expired (or already
// released) has not leaked.
func TestOperationalMetrics(t *testing.T) {
	w := setupWallets(t)
	zctx := backend(t, w.estate, w.zshala)

	var payments []string
	for i := range 3 {
		resp, err := w.svc.CreatePayment(withKey(zctx, fmt.Sprintf("ops-%d", i)), topupRequest(w.zshalaMain.GetId()))
		if err != nil {
			t.Fatalf("payment %d: %v", i, err)
		}
		payments = append(payments, resp.GetPayment().GetId())
	}
	// One open payment is two hours old: the expiry job should have closed it.
	testsupport.Exec(t, `UPDATE payments SET created_at = NOW() - INTERVAL '2 hours' WHERE public_id = $1`, payments[0])
	// Of the others, one captured and one failed: 50% over finished attempts.
	testsupport.Exec(t, `UPDATE payment_attempts SET status = 'captured'
		WHERE payment_id = (SELECT id FROM payments WHERE public_id = $1)`, payments[1])
	testsupport.Exec(t, `UPDATE payments SET status = 'captured', captured_amount = amount, captured_at = NOW() WHERE public_id = $1`, payments[1])
	testsupport.Exec(t, `UPDATE payment_attempts SET status = 'failed', failure_code = 'card_declined'
		WHERE payment_id = (SELECT id FROM payments WHERE public_id = $1)`, payments[2])
	testsupport.Exec(t, `UPDATE payments SET status = 'failed' WHERE public_id = $1`, payments[2])

	// Two webhooks waiting (the older for ten minutes), one already handled.
	testsupport.Exec(t, `INSERT INTO provider_events (provider, event_id, event_type, payload, received_at, processed_at) VALUES
		('mock', 'evt_old', 'payment.captured', '{}', NOW() - INTERVAL '10 minutes', NULL),
		('mock', 'evt_new', 'payment.captured', '{}', NOW() - INTERVAL '1 minute', NULL),
		('mock', 'evt_done', 'payment.captured', '{}', NOW() - INTERVAL '1 hour', NOW())`)

	// Holds: one leaked (active, expired an hour ago), one live, one released.
	testsupport.Exec(t, `INSERT INTO ledger_holds (public_id, external_id, account_id, amount, currency, status, expires_at, resolved_at)
		SELECT 'hld_' || x.n, 'ops:hold:' || x.n, w.ledger_account_id, 100, 'INR', x.status, NOW() + x.expiry,
		       CASE WHEN x.status = 'active' THEN NULL ELSE NOW() END
		FROM wallets w, (VALUES (1, 'active', INTERVAL '-1 hour'), (2, 'active', INTERVAL '1 hour'),
		                        (3, 'released', INTERVAL '-1 hour')) AS x(n, status, expiry)
		WHERE w.public_id = $1`, w.zshalaMain.GetId())

	r, err := opsmetrics.Collect(zctx, repo, opsmetrics.Config{StuckAfter: 40 * time.Minute}, time.Now())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if r.StuckPayments != 1 {
		t.Errorf("stuck payments = %d, want 1 — only the two-hour-old open one", r.StuckPayments)
	}
	if r.EventBacklog != 2 {
		t.Errorf("webhook backlog = %d, want 2 unprocessed", r.EventBacklog)
	}
	if r.OldestEventAge < 9*time.Minute || r.OldestEventAge > 11*time.Minute {
		t.Errorf("oldest webhook waited %s, want about 10m", r.OldestEventAge)
	}
	if r.OverdueHolds != 1 {
		t.Errorf("overdue holds = %d, want 1 — not the live one, not the released one", r.OverdueHolds)
	}
	if len(r.Providers) != 1 {
		t.Fatalf("providers = %v, want the mock alone", r.Providers)
	}
	if rate, ok := opsmetrics.SuccessRateBps(r.Providers[0]); !ok || rate != 5000 {
		t.Errorf("success rate = %d (%t), want 5000: one captured, one failed, the open ones counted for neither", rate, ok)
	}
}
