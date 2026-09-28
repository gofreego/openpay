package postgresql

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

func TestGetAccountViewByIDOrCode(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()
	wallet, _ := fundedWallet(t, repo, 1000)

	if err := placeHold(t, repo, newHold("h", wallet, 300)); err != nil {
		t.Fatalf("place hold: %v", err)
	}

	for _, ref := range []string{wallet.PublicID, wallet.Code} {
		view, err := repo.GetAccountView(ctx, ref)
		if err != nil {
			t.Fatalf("get %q: %v", ref, err)
		}
		if view.Account.ID != wallet.ID {
			t.Errorf("get %q returned account %d, want %d", ref, view.Account.ID, wallet.ID)
		}
		if view.Balance.Natural(wallet.Type) != 1000 || view.Balance.Available(wallet.Type) != 700 {
			t.Errorf("balance %d available %d, want 1000 and 700",
				view.Balance.Natural(wallet.Type), view.Balance.Available(wallet.Type))
		}
	}

	if _, err := repo.GetAccountView(ctx, "wallet:nobody"); !apperrors.Is(err, apperrors.NotFound) {
		t.Errorf("missing account: error code = %q, want %q", apperrors.CodeOf(err), apperrors.NotFound)
	}
}

// Statement pages must not shift as postings arrive: keyset paging continues
// from the last posting seen, however many were added since.
func TestStatementPagesByKeyset(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()
	wallet, income := fundedWallet(t, repo, 1000)

	for i := range 4 {
		if err := post(t, repo, journal(fmt.Sprintf("spend:%d", i),
			leg(wallet, dao.Debit, 100), leg(income, dao.Credit, 100))); err != nil {
			t.Fatalf("spend %d: %v", i, err)
		}
	}

	first, err := repo.ListStatement(ctx, wallet.ID, 2, 0)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 2 || first[0].JournalExternalID != "spend:3" || first[1].JournalExternalID != "spend:2" {
		t.Fatalf("first page = %v, want spend:3, spend:2", externalIDs(first))
	}
	// Newest first, and balance_after is the running balance at each line.
	if got := first[0].Posting.BalanceAfter * wallet.Type.NormalSign(); got != 600 {
		t.Errorf("balance after the newest spend = %d, want 600", got)
	}

	// A new posting arrives between pages; the next page must not repeat spend:2.
	if err := post(t, repo, journal("spend:late", leg(wallet, dao.Debit, 100), leg(income, dao.Credit, 100))); err != nil {
		t.Fatalf("late spend: %v", err)
	}

	second, err := repo.ListStatement(ctx, wallet.ID, 10, first[1].Posting.ID)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if got := externalIDs(second); fmt.Sprint(got) != "[spend:1 spend:0 fund]" {
		t.Errorf("second page = %v, want [spend:1 spend:0 fund]", got)
	}
}

func externalIDs(entries []*dao.StatementEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.JournalExternalID
	}
	return out
}

func TestGetJournalViewShowsWhatItReverses(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()
	wallet, income := fundedWallet(t, repo, 1000)

	original := journal("purchase:1", leg(wallet, dao.Debit, 300), leg(income, dao.Credit, 300))
	if err := post(t, repo, original); err != nil {
		t.Fatalf("purchase: %v", err)
	}
	reversal := journal("purchase:1:reversal", leg(income, dao.Debit, 300), leg(wallet, dao.Credit, 300))
	reversal.Kind = dao.JournalReversal
	reversal.ReversesJournalID = &original.ID
	if err := post(t, repo, reversal); err != nil {
		t.Fatalf("reversal: %v", err)
	}

	for _, ref := range []string{reversal.PublicID, reversal.ExternalID} {
		j, err := repo.GetJournalView(ctx, ref)
		if err != nil {
			t.Fatalf("get %q: %v", ref, err)
		}
		if j.ReversesJournalPublicID == nil || *j.ReversesJournalPublicID != original.PublicID {
			t.Errorf("reverses = %v, want %q", j.ReversesJournalPublicID, original.PublicID)
		}
		if len(j.Postings) != 2 || j.Postings[0].AccountPublicID != income.PublicID {
			t.Errorf("postings = %+v, want two, the first on %q", j.Postings, income.Code)
		}
	}
}

func TestTrialBalance(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()

	zshala := &dao.Product{PublicID: ids.New(ids.Product), Code: "zshala", Name: "Zshala",
		Status: dao.ProductActive, DefaultCurrency: "INR"}
	if err := repo.CreateProduct(ctx, zshala); err != nil {
		t.Fatalf("create product: %v", err)
	}
	psp := account(t, repo, "psp:razorpay:receivable", dao.AccountAsset)
	sales := &dao.LedgerAccount{PublicID: ids.New(ids.LedgerAccount), Code: "income:zshala:product_sales",
		ProductID: &zshala.ID, Type: dao.AccountIncome, Currency: "INR",
		OwnerKind: dao.OwnerMerchant, Status: dao.AccountActive}
	if err := repo.CreateLedgerAccount(ctx, sales); err != nil {
		t.Fatalf("create sales: %v", err)
	}
	account(t, repo, "expense:never_used", dao.AccountExpense)

	if err := post(t, repo, journal("sale:1", leg(psp, dao.Debit, 500), leg(sales, dao.Credit, 500))); err != nil {
		t.Fatalf("sale 1: %v", err)
	}
	between := time.Now()
	time.Sleep(20 * time.Millisecond)
	if err := post(t, repo, journal("sale:2", leg(psp, dao.Debit, 300), leg(sales, dao.Credit, 300))); err != nil {
		t.Fatalf("sale 2: %v", err)
	}

	lines, err := repo.TrialBalance(ctx, nil, time.Now(), false)
	if err != nil {
		t.Fatalf("trial balance: %v", err)
	}
	var debits, credits int64
	for _, l := range lines {
		debits += l.Debits
		credits += l.Credits
	}
	if len(lines) != 2 || debits != 800 || credits != 800 {
		t.Errorf("%d lines, debits %d, credits %d; want 2 lines and 800 each", len(lines), debits, credits)
	}

	withEmpty, err := repo.TrialBalance(ctx, nil, time.Now(), true)
	if err != nil {
		t.Fatalf("trial balance with empty: %v", err)
	}
	if len(withEmpty) != 3 {
		t.Errorf("including empty accounts gave %d lines, want 3", len(withEmpty))
	}

	past, err := repo.TrialBalance(ctx, nil, between, false)
	if err != nil {
		t.Fatalf("past trial balance: %v", err)
	}
	for _, l := range past {
		if l.Debits+l.Credits != 500 {
			t.Errorf("%s as of before sale 2: moved %d, want 500", l.Code, l.Debits+l.Credits)
		}
	}

	product, err := repo.TrialBalance(ctx, &zshala.ID, time.Now(), false)
	if err != nil {
		t.Fatalf("product trial balance: %v", err)
	}
	if len(product) != 1 || product[0].Code != sales.Code || product[0].Raw()*sales.Type.NormalSign() != 800 {
		t.Errorf("product trial balance = %+v, want only %s at 800", product, sales.Code)
	}
}

// "_" is a LIKE wildcard and appears in nearly every account code; a prefix
// filter that does not escape it matches accounts it should not.
func TestListAccountViewsCodePrefixIsLiteral(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()

	account(t, repo, "income:a_b:sales", dao.AccountIncome)
	account(t, repo, "income:aXb:sales", dao.AccountIncome)
	account(t, repo, "psp:razorpay:receivable", dao.AccountAsset)

	views, total, err := repo.ListAccountViews(ctx, &filter.LedgerAccount{CodePrefix: "income:a_"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(views) != 1 || views[0].Account.Code != "income:a_b:sales" {
		t.Errorf("prefix income:a_ matched %d accounts (total %d), want only income:a_b:sales", len(views), total)
	}

	views, _, err = repo.ListAccountViews(ctx, &filter.LedgerAccount{Type: dao.AccountAsset, PlatformOnly: true})
	if err != nil {
		t.Fatalf("list assets: %v", err)
	}
	if len(views) != 1 || views[0].Account.Code != "psp:razorpay:receivable" {
		t.Errorf("platform assets = %d accounts, want psp:razorpay:receivable only", len(views))
	}
}
