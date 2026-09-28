package service_test

import (
	"testing"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/pkg/apperrors"
)

func topupRequest(walletID string) *openpay_v1.CreatePaymentRequest {
	return &openpay_v1.CreatePaymentRequest{
		Purpose: openpay_v1.PaymentPurpose_PAYMENT_PURPOSE_WALLET_TOPUP, WalletId: walletID,
		Amount: 50000, Currency: "INR", Description: "top-up",
	}
}

// A retried create returns the same payment and checkout: a customer who
// double-taps "pay" is not sent to two checkouts.
func TestCreatePaymentIsIdempotent(t *testing.T) {
	w := setupWallets(t)
	ctx := withKey(backend(t, w.estate, w.zshala), "checkout-1")

	first, err := w.svc.CreatePayment(ctx, topupRequest(w.zshalaMain.GetId()))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	p := first.GetPayment()
	if p.GetStatus() != openpay_v1.PaymentStatus_PAYMENT_STATUS_PENDING || p.GetCheckoutUrl() == "" {
		t.Fatalf("payment = %s with checkout %q, want pending with a checkout", p.GetStatus(), p.GetCheckoutUrl())
	}
	again, err := w.svc.CreatePayment(ctx, topupRequest(w.zshalaMain.GetId()))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if again.GetPayment().GetId() != p.GetId() || again.GetPayment().GetCheckoutUrl() != p.GetCheckoutUrl() {
		t.Errorf("retry opened payment %s, want the original %s", again.GetPayment().GetId(), p.GetId())
	}
}

// One product's backend cannot pay into, or read, another product's world.
func TestPaymentsAreScopedToTheirProduct(t *testing.T) {
	w := setupWallets(t)
	zctx := withKey(backend(t, w.estate, w.zshala), "checkout-z")
	bctx := withKey(backend(t, w.estate, w.bappa), "checkout-b")

	_, err := w.svc.CreatePayment(zctx, topupRequest(w.bappaMain.GetId()))
	wantCode(t, "top up another product's wallet", err, apperrors.NotFound)

	created, err := w.svc.CreatePayment(zctx, topupRequest(w.zshalaMain.GetId()))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = w.svc.GetPayment(bctx, &openpay_v1.GetPaymentRequest{Id: created.GetPayment().GetId()})
	wantCode(t, "read another product's payment", err, apperrors.NotFound)

	_, err = w.svc.GetPayment(as(auth.PermPaymentsRead, auth.PermScopeProductPrefix+"bappaapp"),
		&openpay_v1.GetPaymentRequest{Id: created.GetPayment().GetId()})
	wantCode(t, "bappaapp operator reading a zshala payment", err, apperrors.NotFound)

	// An operator in scope sees the full timeline.
	detail, err := w.svc.GetPayment(as(auth.PermPaymentsRead, auth.PermScopeProductPrefix+"zshala"),
		&openpay_v1.GetPaymentRequest{Id: created.GetPayment().GetId()})
	if err != nil {
		t.Fatalf("operator get: %v", err)
	}
	if len(detail.GetPayment().GetAttempts()) != 1 || len(detail.GetPayment().GetTransitions()) != 1 {
		t.Errorf("operator view has %d attempts and %d transitions, want 1 and 1",
			len(detail.GetPayment().GetAttempts()), len(detail.GetPayment().GetTransitions()))
	}
}

// Orders do not exist yet, so neither do payments for them.
func TestOrderPaymentsAreRefusedUntilOrdersExist(t *testing.T) {
	w := setupWallets(t)
	req := topupRequest(w.zshalaMain.GetId())
	req.Purpose = openpay_v1.PaymentPurpose_PAYMENT_PURPOSE_ORDER
	_, err := w.svc.CreatePayment(withKey(backend(t, w.estate, w.zshala), "order-1"), req)
	wantCode(t, "order payment", err, apperrors.InvalidArgument)
}

// Refunding a top-up returns closed-loop money to a card, so only central ops
// may do it: not the product's backend, not its operators.
func TestRefundsAreCentralOnly(t *testing.T) {
	w := setupWallets(t)
	created, err := w.svc.CreatePayment(withKey(backend(t, w.estate, w.zshala), "checkout-r"), topupRequest(w.zshalaMain.GetId()))
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	req := &openpay_v1.CreateRefundRequest{PaymentId: created.GetPayment().GetId(), Amount: 100,
		ReasonCode: "customer_request", Memo: "changed their mind"}

	_, err = w.svc.CreateRefund(withKey(backend(t, w.estate, w.zshala), "refund-1"), req)
	wantCode(t, "product backend refunding", err, apperrors.PermissionDenied)

	_, err = w.svc.CreateRefund(as(auth.PermRefundsCreate, auth.PermScopeProductPrefix+"zshala"), req)
	wantCode(t, "product operator refunding", err, apperrors.PermissionDenied)

	// Central ops are allowed to try; this payment is unpaid, so the engine refuses it.
	_, err = w.svc.CreateRefund(withKey(as(auth.PermRefundsCreate, auth.PermScopeAll), "refund-2"), req)
	wantCode(t, "refunding an unpaid payment", err, apperrors.FailedPrecondition)
}
