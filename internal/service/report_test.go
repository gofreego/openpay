package service_test

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/service"
	"github.com/gofreego/openpay/internal/testsupport"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// around is a period containing now.
func around() (*timestamppb.Timestamp, *timestamppb.Timestamp) {
	now := time.Now()
	return timestamppb.New(now.Add(-time.Hour)), timestamppb.New(now.Add(time.Hour))
}

func grant(t *testing.T, w walletEstate, wallet *openpay_v1.Wallet, product *openpay_v1.Product, amount int64, memo string) {
	t.Helper()
	ctx := withKey(backend(t, w.estate, product), fmt.Sprintf("grant-%s-%d-%s", wallet.GetId(), amount, memo))
	if _, err := w.svc.GrantWallet(ctx, &openpay_v1.GrantWalletRequest{
		WalletId: wallet.GetId(), Amount: amount, ReasonCode: "promotion", Memo: memo}); err != nil {
		t.Fatalf("grant %d: %v", amount, err)
	}
}

func pnlFor(t *testing.T, resp *openpay_v1.GetProductPnLResponse, productID string) *openpay_v1.ProductPnL {
	t.Helper()
	for _, p := range resp.GetProducts() {
		if p.GetProductId() == productID {
			return p
		}
	}
	return nil
}

// Promotions granted show up as that product's expense in the period, and
// only that product's; outside the period they are not there at all.
func TestProductPnL(t *testing.T) {
	w := setupWallets(t)
	grant(t, w, w.zshalaBonus, w.zshala, 5000, "welcome")
	grant(t, w, w.zshalaBonus, w.zshala, 700, "referral")

	from, to := around()
	resp, err := w.svc.GetProductPnL(central(), &openpay_v1.GetProductPnLRequest{From: from, To: to})
	if err != nil {
		t.Fatalf("pnl: %v", err)
	}
	z := pnlFor(t, resp, w.zshala.GetId())
	if z == nil {
		t.Fatalf("no line for zshala in %v", resp)
	}
	if z.GetExpense() != 5700 || z.GetIncome() != 0 || z.GetNet() != -5700 {
		t.Errorf("zshala income %d expense %d net %d, want 0 / 5700 / -5700", z.GetIncome(), z.GetExpense(), z.GetNet())
	}
	found := false
	for _, l := range z.GetLines() {
		if l.GetAccountCode() == "expense:zshala:promotions" {
			found = l.GetAmount() == 5700
		}
	}
	if !found {
		t.Errorf("expense:zshala:promotions is not 5700 in %v", z.GetLines())
	}
	if b := pnlFor(t, resp, w.bappa.GetId()); b == nil || b.GetExpense() != 0 {
		t.Errorf("bappaapp = %v, want its lines at zero — zshala's grants are not its cost", b)
	}

	// A product operator sees their own product and is refused another's.
	own, err := w.svc.GetProductPnL(productOps("zshala"), &openpay_v1.GetProductPnLRequest{From: from, To: to})
	if err != nil {
		t.Fatalf("product pnl: %v", err)
	}
	if len(own.GetProducts()) != 1 || own.GetProducts()[0].GetProductId() != w.zshala.GetId() {
		t.Errorf("zshala operator saw %d products, want only zshala", len(own.GetProducts()))
	}
	_, err = w.svc.GetProductPnL(productOps("zshala"), &openpay_v1.GetProductPnLRequest{From: from, To: to, ProductId: w.bappa.GetId()})
	wantCode(t, "another product's P&L", err, apperrors.PermissionDenied)

	// Yesterday, nothing happened.
	past, err := w.svc.GetProductPnL(central(), &openpay_v1.GetProductPnLRequest{
		From: timestamppb.New(time.Now().Add(-48 * time.Hour)), To: timestamppb.New(time.Now().Add(-24 * time.Hour))})
	if err != nil {
		t.Fatalf("past pnl: %v", err)
	}
	if z := pnlFor(t, past, w.zshala.GetId()); z == nil || z.GetExpense() != 0 {
		t.Errorf("a period before the grants shows %v, want zero", z)
	}

	_, err = w.svc.GetProductPnL(central(), &openpay_v1.GetProductPnLRequest{From: to, To: from})
	wantCode(t, "backwards period", err, apperrors.InvalidArgument)
	_, err = w.svc.GetProductPnL(backend(t, w.estate, w.zshala), &openpay_v1.GetProductPnLRequest{From: from, To: to})
	wantCode(t, "a product backend asking for a P&L", err, apperrors.PermissionDenied)
}

func exportRows(t *testing.T, body []byte) [][]string {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("export is not valid CSV: %v\n%s", err, body)
	}
	return rows
}

// A wallet statement exports oldest first with running balances, across
// pages, with caller text made safe for a spreadsheet — and a period over the
// cap is refused rather than silently cut short.
func TestWalletStatementExport(t *testing.T) {
	w := setupWallets(t)
	restore := service.SetExportLimits(2, 5)
	defer restore()

	grant(t, w, w.zshalaBonus, w.zshala, 100, "first")
	grant(t, w, w.zshalaBonus, w.zshala, 200, `=HYPERLINK("http://evil.test","click")`)
	grant(t, w, w.zshalaBonus, w.zshala, 300, "third")

	from, to := around()
	zctx := backend(t, w.estate, w.zshala)
	body, err := w.svc.ExportWalletStatement(zctx, &openpay_v1.ExportWalletStatementRequest{
		WalletId: w.zshalaBonus.GetId(), From: from, To: to})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if body.GetContentType() != "text/csv; charset=utf-8" {
		t.Errorf("content type %q", body.GetContentType())
	}
	rows := exportRows(t, body.GetData())
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want a header and 3 entries (paging at 2 must not drop or repeat any):\n%s", len(rows), body.GetData())
	}
	header := map[string]int{}
	for i, h := range rows[0] {
		header[h] = i
	}
	wantBalances := []string{"1.00", "3.00", "6.00"}
	for i, row := range rows[1:] {
		if got := row[header["balance_after"]]; got != wantBalances[i] {
			t.Errorf("row %d balance_after = %s, want %s (oldest first, running)", i+1, got, wantBalances[i])
		}
	}
	if memo := rows[2][header["memo"]]; memo[0] != '\'' {
		t.Errorf("memo %q would run as a formula in a spreadsheet", memo)
	}

	// Outside the period: a header and nothing else.
	empty, err := w.svc.ExportWalletStatement(zctx, &openpay_v1.ExportWalletStatementRequest{
		WalletId: w.zshalaBonus.GetId(), From: timestamppb.New(time.Now().Add(-48 * time.Hour)),
		To: timestamppb.New(time.Now().Add(-24 * time.Hour))})
	if err != nil {
		t.Fatalf("empty export: %v", err)
	}
	if n := len(exportRows(t, empty.GetData())); n != 1 {
		t.Errorf("an empty period exported %d rows, want just the header", n)
	}

	// Another product's wallet does not exist, as far as zshala knows.
	_, err = w.svc.ExportWalletStatement(zctx, &openpay_v1.ExportWalletStatementRequest{
		WalletId: w.bappaMain.GetId(), From: from, To: to})
	wantCode(t, "export another product's wallet", err, apperrors.NotFound)

	// Over the cap: refused, not truncated.
	restore()
	restore = service.SetExportLimits(2, 2)
	_, err = w.svc.ExportWalletStatement(zctx, &openpay_v1.ExportWalletStatementRequest{
		WalletId: w.zshalaBonus.GetId(), From: from, To: to})
	wantCode(t, "export over the row cap", err, apperrors.FailedPrecondition)
}

// An account statement export sees the account in its natural direction:
// promotions expense grows with each grant.
func TestAccountStatementExport(t *testing.T) {
	w := setupWallets(t)
	grant(t, w, w.zshalaBonus, w.zshala, 250, "one")

	from, to := around()
	body, err := w.svc.ExportAccountStatement(central(), &openpay_v1.ExportAccountStatementRequest{
		AccountId: "expense:zshala:promotions", From: from, To: to})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	rows := exportRows(t, body.GetData())
	if len(rows) != 2 || rows[1][7] != "250" || rows[1][8] != "250" {
		t.Fatalf("rows = %v, want one entry changing and ending at 250", rows)
	}

	_, err = w.svc.ExportAccountStatement(productOps("bappaapp"), &openpay_v1.ExportAccountStatementRequest{
		AccountId: "expense:zshala:promotions", From: from, To: to})
	wantCode(t, "another product's account", err, apperrors.NotFound)
}

// Success rate counts captured over finished attempts; open ones count for
// neither side, and failures are grouped by code.
func TestProviderStats(t *testing.T) {
	w := setupWallets(t)
	zctx := backend(t, w.estate, w.zshala)

	var payments []string
	for i := range 5 {
		resp, err := w.svc.CreatePayment(withKey(zctx, fmt.Sprintf("stats-%d", i)), topupRequest(w.zshalaMain.GetId()))
		if err != nil {
			t.Fatalf("create payment %d: %v", i, err)
		}
		payments = append(payments, resp.GetPayment().GetId())
	}
	// Outcomes are set directly: this tests the arithmetic, and the payment
	// engine's own tests cover how attempts get there.
	set := func(payment, status, code string) {
		testsupport.Exec(t, `UPDATE payment_attempts SET status = $2, failure_code = NULLIF($3, '')
			WHERE payment_id = (SELECT id FROM payments WHERE public_id = $1)`, payment, status, code)
		if status == "captured" {
			testsupport.Exec(t, `UPDATE payments SET status = 'captured', captured_amount = amount, captured_at = NOW()
				WHERE public_id = $1`, payment)
		}
	}
	set(payments[0], "captured", "")
	set(payments[1], "captured", "")
	set(payments[2], "failed", "card_declined")
	set(payments[3], "failed", "card_declined")
	// payments[4] stays open.

	from, to := around()
	resp, err := w.svc.GetProviderStats(central(), &openpay_v1.GetProviderStatsRequest{From: from, To: to})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(resp.GetProviders()) != 1 {
		t.Fatalf("providers = %v, want the mock alone", resp.GetProviders())
	}
	s := resp.GetProviders()[0]
	if s.GetAttempts() != 5 || s.GetCaptured() != 2 || s.GetFailed() != 2 || s.GetOpen() != 1 {
		t.Errorf("counts = %v", s)
	}
	if s.GetSuccessRateBps() != 5000 {
		t.Errorf("success rate = %d bps, want 5000: 2 of 4 finished, the open one counts for neither", s.GetSuccessRateBps())
	}
	if s.GetCapturedAmount() != 100000 {
		t.Errorf("captured amount = %d, want 100000", s.GetCapturedAmount())
	}
	if len(s.GetTopFailures()) != 1 || s.GetTopFailures()[0].GetCode() != "card_declined" || s.GetTopFailures()[0].GetCount() != 2 {
		t.Errorf("top failures = %v, want card_declined x2", s.GetTopFailures())
	}

	// BappaApp's operators see none of zshala's traffic.
	other, err := w.svc.GetProviderStats(productOps("bappaapp"), &openpay_v1.GetProviderStatsRequest{From: from, To: to})
	if err != nil {
		t.Fatalf("bappa stats: %v", err)
	}
	if len(other.GetProviders()) != 0 {
		t.Errorf("bappaapp operator saw %v", other.GetProviders())
	}
	_, err = w.svc.GetProviderStats(as(auth.PermLedgerRead, auth.PermScopeAll), &openpay_v1.GetProviderStatsRequest{From: from, To: to})
	wantCode(t, "stats without payments:read", err, apperrors.PermissionDenied)
}
