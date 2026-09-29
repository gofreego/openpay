package service_test

import (
	"testing"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// The activity feed carries the provider calls made for a payment; the
// listings respect scope like every other read.
func TestConsoleReads(t *testing.T) {
	w := setupWallets(t)
	zctx := withKey(backend(t, w.estate, w.zshala), "console-1")
	created, err := w.svc.CreatePayment(zctx, topupRequest(w.zshalaMain.GetId()))
	if err != nil {
		t.Fatalf("payment: %v", err)
	}
	id := created.GetPayment().GetId()

	activity, err := w.svc.GetPaymentActivity(central(), &openpay_v1.GetPaymentActivityRequest{Id: id})
	if err != nil {
		t.Fatalf("activity: %v", err)
	}
	if len(activity.GetRequests()) == 0 || activity.GetRequests()[0].GetOperation() != "create_payment" {
		t.Errorf("activity requests = %v, want the create_payment call", activity.GetRequests())
	}
	_, err = w.svc.GetPaymentActivity(productOps("bappaapp"), &openpay_v1.GetPaymentActivityRequest{Id: id})
	wantCode(t, "another product's payment activity", err, apperrors.NotFound)

	orders, err := w.svc.ListOrders(central(), &openpay_v1.ListOrdersRequest{})
	if err != nil || orders.GetTotal() != 0 {
		t.Errorf("orders = %v (%v), want none yet", orders, err)
	}
	_, err = w.svc.ListOrders(productOps("zshala"), &openpay_v1.ListOrdersRequest{ProductId: w.bappa.GetId()})
	wantCode(t, "orders for another product", err, apperrors.PermissionDenied)
	if _, err := w.svc.SearchRefunds(central(), &openpay_v1.SearchRefundsRequest{}); err != nil {
		t.Errorf("refunds: %v", err)
	}

	// Product creation is a platform entry: central ops see it, a product
	// operator does not.
	all, err := w.svc.ListAuditLog(as(auth.PermAuditRead, auth.PermScopeAll), &openpay_v1.ListAuditLogRequest{})
	if err != nil || len(all.GetEntries()) == 0 {
		t.Fatalf("central audit = %v (%v), want entries", all, err)
	}
	scoped, err := w.svc.ListAuditLog(as(auth.PermAuditRead, auth.PermScopeProductPrefix+"zshala"), &openpay_v1.ListAuditLogRequest{})
	if err != nil {
		t.Fatalf("scoped audit: %v", err)
	}
	for _, e := range scoped.GetEntries() {
		if e.GetProductId() != w.zshala.GetId() {
			t.Errorf("zshala operator saw audit entry %s for product %q", e.GetAction(), e.GetProductId())
		}
	}
	_, err = w.svc.ListAuditLog(central(), &openpay_v1.ListAuditLogRequest{})
	wantCode(t, "audit without audit:read", err, apperrors.PermissionDenied)
}
