package service

import (
	"context"
	"sort"
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// The ledger over the API is read-only. Every method here is a read except
// RunLedgerCheck, which records its own result but moves no money.

func (s *Service) GetLedgerAccount(ctx context.Context, req *openpay_v1.GetLedgerAccountRequest) (*openpay_v1.GetLedgerAccountResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermLedgerRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	view, err := s.repo.GetAccountView(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if err := requireVisible(scope, view.Account.ProductID, "ledger account", req.GetId()); err != nil {
		return nil, err
	}
	return &openpay_v1.GetLedgerAccountResponse{Account: toProtoLedgerAccount(view)}, nil
}

func (s *Service) ListLedgerAccounts(ctx context.Context, req *openpay_v1.ListLedgerAccountsRequest) (*openpay_v1.ListLedgerAccountsResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermLedgerRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	if req.GetProductId() != "" && req.GetPlatformOnly() {
		return nil, apperrors.New(apperrors.InvalidArgument,
			"product_id and platform_only are contradictory: platform accounts belong to no product")
	}

	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetPlatformOnly() && !scope.All() {
		return nil, apperrors.New(apperrors.PermissionDenied,
			"platform accounts are visible only with the %q scope", auth.PermScopeAll)
	}

	f := &filter.LedgerAccount{
		Limit:        int(req.GetLimit()),
		Offset:       int(req.GetOffset()),
		PlatformOnly: req.GetPlatformOnly(),
		Type:         fromProtoAccountType(req.GetType()),
		CodePrefix:   req.GetCodePrefix(),
		Scope:        scope,
	}
	if req.GetProductId() != "" {
		product, err := s.repo.GetProductByPublicID(ctx, req.GetProductId())
		if err != nil {
			return nil, err
		}
		if err := requireProductInScope(scope, product.ID); err != nil {
			return nil, err
		}
		f.ProductID = &product.ID
	}

	views, total, err := s.repo.ListAccountViews(ctx, f)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.ListLedgerAccountsResponse{Total: total}
	for _, view := range views {
		response.Accounts = append(response.Accounts, toProtoLedgerAccount(view))
	}
	return response, nil
}

func (s *Service) GetAccountStatement(ctx context.Context, req *openpay_v1.GetAccountStatementRequest) (*openpay_v1.GetAccountStatementResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermLedgerRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	view, err := s.repo.GetAccountView(ctx, req.GetAccountId())
	if err != nil {
		return nil, err
	}
	account := view.Account
	if err := requireVisible(scope, account.ProductID, "ledger account", req.GetAccountId()); err != nil {
		return nil, err
	}

	entries, next, err := s.statementPage(ctx, account.ID, account.Type, req.GetLimit(), req.GetCursor())
	if err != nil {
		return nil, err
	}
	return &openpay_v1.GetAccountStatementResponse{
		Account:    toProtoLedgerAccount(view),
		Entries:    entries,
		NextCursor: next,
	}, nil
}

// statementPage reads one page of an account's statement, newest first.
//
// The cursor is the id of the last posting shown. It is opaque to callers but
// not secret: a posting id reveals nothing a statement does not.
func (s *Service) statementPage(ctx context.Context, accountID int64, accountType dao.AccountType, limit32 int32, cursor string) ([]*openpay_v1.StatementEntry, string, error) {
	var before int64
	if cursor != "" {
		var err error
		if before, err = strconv.ParseInt(cursor, 10, 64); err != nil || before <= 0 {
			return nil, "", apperrors.New(apperrors.InvalidArgument, "cursor %q is not one this API issued", cursor)
		}
	}
	limit := int(limit32)
	if limit <= 0 {
		limit = 50
	}

	// One extra row says whether an older page exists without a count query.
	rows, err := s.repo.ListStatement(ctx, accountID, limit+1, before)
	if err != nil {
		return nil, "", err
	}
	var next string
	if len(rows) > limit {
		rows = rows[:limit]
		next = strconv.FormatInt(rows[limit-1].Posting.ID, 10)
	}

	sign := accountType.NormalSign()
	entries := make([]*openpay_v1.StatementEntry, 0, len(rows))
	for _, e := range rows {
		entries = append(entries, &openpay_v1.StatementEntry{
			JournalId:         e.JournalPublicID,
			JournalExternalId: e.JournalExternalID,
			JournalKind:       string(e.JournalKind),
			Memo:              e.JournalMemo,
			Direction:         toProtoDirection(e.Posting.Direction),
			Amount:            e.Posting.Amount,
			Change:            e.Posting.Signed() * sign,
			BalanceAfter:      e.Posting.BalanceAfter * sign,
			PostedAt:          timestamppb.New(e.JournalPostedAt),
		})
	}
	return entries, next, nil
}

func (s *Service) GetJournal(ctx context.Context, req *openpay_v1.GetJournalRequest) (*openpay_v1.GetJournalResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermLedgerRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	j, err := s.repo.GetJournalView(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	// A product's journal may post to platform accounts (a payment debits the
	// PSP receivable), and its operators may see those legs: it is their
	// money's movement. A platform journal — a settlement spanning products —
	// is central-only.
	if err := requireVisible(scope, j.ProductID, "journal", req.GetId()); err != nil {
		return nil, err
	}

	out := &openpay_v1.Journal{
		Id:                j.PublicID,
		ExternalId:        j.ExternalID,
		Kind:              string(j.Kind),
		ProductId:         deref(j.ProductPublicID),
		SourceKind:        deref(j.SourceKind),
		SourceId:          deref(j.SourceID),
		ReversesJournalId: deref(j.ReversesJournalPublicID),
		Memo:              j.Memo,
		PostedAt:          timestamppb.New(j.PostedAt),
	}
	for _, p := range j.Postings {
		out.Postings = append(out.Postings, &openpay_v1.JournalPosting{
			AccountId:   p.AccountPublicID,
			AccountCode: p.AccountCode,
			Direction:   toProtoDirection(p.Direction),
			Amount:      p.Amount,
			Currency:    p.Currency,
		})
	}
	return &openpay_v1.GetJournalResponse{Journal: out}, nil
}

func (s *Service) GetTrialBalance(ctx context.Context, req *openpay_v1.GetTrialBalanceRequest) (*openpay_v1.GetTrialBalanceResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermLedgerRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	asOf := time.Now()
	if req.GetAsOf() != nil {
		if err := req.GetAsOf().CheckValid(); err != nil {
			return nil, apperrors.New(apperrors.InvalidArgument, "as_of is not a valid timestamp")
		}
		asOf = req.GetAsOf().AsTime()
	}

	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	var productID *int64
	if req.GetProductId() != "" {
		product, err := s.repo.GetProductByPublicID(ctx, req.GetProductId())
		if err != nil {
			return nil, err
		}
		if err := requireProductInScope(scope, product.ID); err != nil {
			return nil, err
		}
		productID = &product.ID
	} else if !scope.All() {
		return nil, apperrors.New(apperrors.PermissionDenied,
			"the platform-wide trial balance requires the %q scope; pass product_id for one product", auth.PermScopeAll)
	}

	lines, err := s.repo.TrialBalance(ctx, productID, asOf, req.GetIncludeEmpty())
	if err != nil {
		return nil, err
	}

	response := &openpay_v1.GetTrialBalanceResponse{AsOf: timestamppb.New(asOf)}
	totals := map[string]*openpay_v1.TrialBalanceTotal{}
	for _, l := range lines {
		response.Lines = append(response.Lines, &openpay_v1.TrialBalanceLine{
			AccountId:   l.AccountPublicID,
			AccountCode: l.Code,
			Type:        toProtoAccountType(l.Type),
			ProductId:   deref(l.ProductPublicID),
			Currency:    l.Currency,
			Debits:      l.Debits,
			Credits:     l.Credits,
			RawBalance:  l.Raw(),
			Balance:     l.Raw() * l.Type.NormalSign(),
		})

		total, ok := totals[l.Currency]
		if !ok {
			total = &openpay_v1.TrialBalanceTotal{Currency: l.Currency}
			totals[l.Currency] = total
		}
		total.Debits += l.Debits
		total.Credits += l.Credits
		total.Difference = total.Debits - total.Credits
	}
	for _, total := range totals {
		response.Totals = append(response.Totals, total)
	}
	sort.Slice(response.Totals, func(i, j int) bool { return response.Totals[i].Currency < response.Totals[j].Currency })
	return response, nil
}

func (s *Service) RunLedgerCheck(ctx context.Context, req *openpay_v1.RunLedgerCheckRequest) (*openpay_v1.RunLedgerCheckResponse, error) {
	if err := auth.RequirePlatformOperator(ctx, auth.PermLedgerCheck); err != nil {
		return nil, err
	}

	caller, _ := appcontext.CallerFrom(ctx)
	triggeredBy := caller.UserID
	run, err := ledger.RunCheck(ctx, s.repo, dao.LedgerCheckManual, &triggeredBy)
	if err != nil {
		return nil, err
	}
	// A run that found drift, or could not complete, is still a successful
	// call: the caller asked for the ledger's state and is getting it.
	return &openpay_v1.RunLedgerCheckResponse{Run: toProtoCheckRun(run)}, nil
}

func (s *Service) ListLedgerCheckRuns(ctx context.Context, req *openpay_v1.ListLedgerCheckRunsRequest) (*openpay_v1.ListLedgerCheckRunsResponse, error) {
	// Platform-level: a check run covers the whole ledger, and its findings name
	// accounts of every product.
	if err := auth.RequirePlatformOperator(ctx, auth.PermLedgerRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	runs, err := s.repo.ListLedgerCheckRuns(ctx, limit)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.ListLedgerCheckRunsResponse{}
	for _, run := range runs {
		response.Runs = append(response.Runs, toProtoCheckRun(run))
	}
	return response, nil
}

func toProtoLedgerAccount(v *dao.AccountView) *openpay_v1.LedgerAccount {
	a := v.Account
	return &openpay_v1.LedgerAccount{
		Id:            a.PublicID,
		Code:          a.Code,
		ProductId:     deref(v.ProductPublicID),
		Type:          toProtoAccountType(a.Type),
		Currency:      a.Currency,
		OwnerKind:     string(a.OwnerKind),
		OwnerId:       deref(a.OwnerID),
		AllowNegative: a.AllowNegative,
		Status:        string(a.Status),
		Balance: &openpay_v1.LedgerBalance{
			Balance:   v.Balance.Natural(a.Type),
			Held:      v.Balance.Held,
			Available: v.Balance.Available(a.Type),
		},
		CreatedAt: timestamppb.New(a.CreatedAt),
	}
}

func toProtoCheckRun(run *dao.LedgerCheckRun) *openpay_v1.LedgerCheckRun {
	out := &openpay_v1.LedgerCheckRun{
		Id:          run.PublicID,
		Status:      toProtoCheckStatus(run.Status),
		Trigger:     string(run.Trigger),
		TriggeredBy: deref(run.TriggeredBy),
		Truncated:   run.Truncated,
		Error:       deref(run.Error),
		StartedAt:   timestamppb.New(run.StartedAt),
		FinishedAt:  timestamppb.New(run.FinishedAt),
	}
	for _, f := range run.Findings {
		out.Findings = append(out.Findings, &openpay_v1.LedgerCheckFinding{
			Invariant: f.Invariant,
			Subject:   f.Subject,
			Expected:  f.Expected,
			Actual:    f.Actual,
			Detail:    f.Detail,
		})
	}
	return out
}

func toProtoCheckStatus(s dao.LedgerCheckStatus) openpay_v1.LedgerCheckStatus {
	switch s {
	case dao.LedgerCheckOK:
		return openpay_v1.LedgerCheckStatus_LEDGER_CHECK_STATUS_OK
	case dao.LedgerCheckDrift:
		return openpay_v1.LedgerCheckStatus_LEDGER_CHECK_STATUS_DRIFT
	case dao.LedgerCheckError:
		return openpay_v1.LedgerCheckStatus_LEDGER_CHECK_STATUS_ERROR
	default:
		return openpay_v1.LedgerCheckStatus_LEDGER_CHECK_STATUS_UNSPECIFIED
	}
}

var accountTypes = map[dao.AccountType]openpay_v1.LedgerAccountType{
	dao.AccountAsset:     openpay_v1.LedgerAccountType_LEDGER_ACCOUNT_TYPE_ASSET,
	dao.AccountLiability: openpay_v1.LedgerAccountType_LEDGER_ACCOUNT_TYPE_LIABILITY,
	dao.AccountEquity:    openpay_v1.LedgerAccountType_LEDGER_ACCOUNT_TYPE_EQUITY,
	dao.AccountIncome:    openpay_v1.LedgerAccountType_LEDGER_ACCOUNT_TYPE_INCOME,
	dao.AccountExpense:   openpay_v1.LedgerAccountType_LEDGER_ACCOUNT_TYPE_EXPENSE,
}

func toProtoAccountType(t dao.AccountType) openpay_v1.LedgerAccountType {
	return accountTypes[t]
}

// fromProtoAccountType maps unspecified to "", which a listing reads as "any".
func fromProtoAccountType(t openpay_v1.LedgerAccountType) dao.AccountType {
	for daoType, protoType := range accountTypes {
		if protoType == t {
			return daoType
		}
	}
	return ""
}

func toProtoDirection(d dao.Direction) openpay_v1.PostingDirection {
	if d == dao.Debit {
		return openpay_v1.PostingDirection_POSTING_DIRECTION_DEBIT
	}
	return openpay_v1.PostingDirection_POSTING_DIRECTION_CREDIT
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
