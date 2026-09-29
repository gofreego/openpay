package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/api/httpbody"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/money"

	"github.com/gofreego/goutils/logger"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Variables rather than constants only so tests can exercise paging and the
// cap without writing fifty thousand postings.
var (
	// maxExportRows caps one statement export. A period with more is refused,
	// not truncated: a cut-short statement looks complete and is not.
	maxExportRows = 50_000
	// exportPage is how many postings each read of an export fetches.
	exportPage = 1_000
)

func (s *Service) GetProductPnL(ctx context.Context, req *openpay_v1.GetProductPnLRequest) (*openpay_v1.GetProductPnLResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermLedgerRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	from, to, err := period(req.GetFrom(), req.GetTo())
	if err != nil {
		return nil, err
	}
	scope, err := s.reportScope(ctx, req.GetProductId())
	if err != nil {
		return nil, err
	}

	lines, err := s.repo.ProductPnL(ctx, scope, from, to)
	if err != nil {
		return nil, err
	}

	// Lines arrive ordered by product then currency, so each run is one
	// report; a product never adds rupees to anything else.
	response := &openpay_v1.GetProductPnLResponse{}
	var current *openpay_v1.ProductPnL
	for _, l := range lines {
		if current == nil || current.ProductId != l.ProductPublicID || current.Currency != l.Currency {
			current = &openpay_v1.ProductPnL{ProductId: l.ProductPublicID, ProductCode: l.ProductCode, Currency: l.Currency}
			response.Products = append(response.Products, current)
		}
		current.Lines = append(current.Lines, &openpay_v1.PnLLine{
			AccountCode: l.AccountCode, Type: string(l.Type), Amount: l.Amount,
		})
		if l.Type == dao.AccountIncome {
			current.Income += l.Amount
		} else {
			current.Expense += l.Amount
		}
		current.Net = current.Income - current.Expense
	}
	return response, nil
}

func (s *Service) GetProviderStats(ctx context.Context, req *openpay_v1.GetProviderStatsRequest) (*openpay_v1.GetProviderStatsResponse, error) {
	if err := auth.RequireOperator(ctx, auth.PermPaymentsRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	from, to, err := period(req.GetFrom(), req.GetTo())
	if err != nil {
		return nil, err
	}
	scope, err := s.reportScope(ctx, req.GetProductId())
	if err != nil {
		return nil, err
	}

	stats, err := s.repo.ProviderStats(ctx, scope, from, to)
	if err != nil {
		return nil, err
	}
	response := &openpay_v1.GetProviderStatsResponse{}
	for _, st := range stats {
		out := &openpay_v1.ProviderStats{
			Provider: st.Provider, Attempts: st.Attempts, Captured: st.Captured, Failed: st.Failed,
			Expired: st.Expired, Cancelled: st.Cancelled, Open: st.Open,
			SuccessRateBps: successRateBps(st), CapturedAmount: st.CapturedAmount,
		}
		for _, f := range st.TopFailures {
			out.TopFailures = append(out.TopFailures, &openpay_v1.FailureCount{Code: f.Code, Count: f.Count})
		}
		response.Providers = append(response.Providers, out)
	}
	return response, nil
}

// successRateBps is captured over finished attempts. Open attempts are left
// out of both sides: counting them as failures would make a busy minute look
// like an outage.
func successRateBps(st *dao.ProviderStats) int32 {
	finished := st.Attempts - st.Open
	if finished <= 0 {
		return 0
	}
	return int32(st.Captured * 10_000 / finished)
}

func (s *Service) ExportAccountStatement(ctx context.Context, req *openpay_v1.ExportAccountStatementRequest) (*httpbody.HttpBody, error) {
	if err := auth.RequireOperator(ctx, auth.PermLedgerRead); err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	from, to, err := period(req.GetFrom(), req.GetTo())
	if err != nil {
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
	return s.exportStatement(ctx, account.ID, account.Type.NormalSign(), account.PublicID, from, to)
}

func (s *Service) ExportWalletStatement(ctx context.Context, req *openpay_v1.ExportWalletStatementRequest) (*httpbody.HttpBody, error) {
	viewer, err := s.walletViewerFor(ctx, auth.PermWalletsRead)
	if err != nil {
		return nil, err
	}
	if err := validate(req); err != nil {
		return nil, err
	}
	from, to, err := period(req.GetFrom(), req.GetTo())
	if err != nil {
		return nil, err
	}
	w, err := s.visibleWallet(ctx, viewer, req.GetWalletId())
	if err != nil {
		return nil, err
	}
	return s.exportStatement(ctx, w.LedgerAccountID, dao.AccountLiability.NormalSign(), w.PublicID, from, to)
}

// exportStatement renders an account's postings in [from, to) as CSV, oldest
// first. It reads the whole period before answering so that a period over the
// cap is refused outright instead of arriving half-written.
func (s *Service) exportStatement(ctx context.Context, accountID int64, sign int64, name string, from, to time.Time) (*httpbody.HttpBody, error) {
	var buf bytes.Buffer
	out := csv.NewWriter(&buf)
	_ = out.Write([]string{
		"posted_at", "journal_id", "external_id", "kind", "memo", "direction",
		"amount_minor", "change_minor", "balance_after_minor", "change", "balance_after", "currency",
	})

	decimal := func(minor int64, currency string) string {
		a, err := money.New(minor, currency)
		if err != nil {
			return strconv.FormatInt(minor, 10)
		}
		return a.Decimal()
	}

	var after int64
	rows := 0
	for {
		page, err := s.repo.ListStatementRange(ctx, accountID, from, to, after, exportPage)
		if err != nil {
			return nil, err
		}
		for _, e := range page {
			rows++
			if rows > maxExportRows {
				return nil, apperrors.New(apperrors.FailedPrecondition,
					"this period has more than %d entries; export a shorter period", maxExportRows)
			}
			p := e.Posting
			change := p.Signed() * sign
			balance := p.BalanceAfter * sign
			_ = out.Write([]string{
				e.JournalPostedAt.UTC().Format(time.RFC3339),
				e.JournalPublicID,
				csvText(e.JournalExternalID),
				string(e.JournalKind),
				csvText(e.JournalMemo),
				p.Direction.String(),
				strconv.FormatInt(p.Amount, 10),
				strconv.FormatInt(change, 10),
				strconv.FormatInt(balance, 10),
				decimal(change, p.Currency),
				decimal(balance, p.Currency),
				p.Currency,
			})
			after = p.ID
		}
		if len(page) < exportPage {
			break
		}
	}
	out.Flush()
	if err := out.Error(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to write statement CSV")
	}

	filename := fmt.Sprintf("%s_%s_%s.csv", name, from.UTC().Format("20060102"), to.UTC().Format("20060102"))
	if err := grpc.SetHeader(ctx, metadata.Pairs(appcontext.HeaderContentDisposition, `attachment; filename="`+filename+`"`)); err != nil {
		// Only a missing transport stream (a direct in-process call, as in
		// tests) gets here; the file is still right, just unnamed.
		logger.Debug(ctx, "could not name export %s: %v", filename, err)
	}
	return &httpbody.HttpBody{ContentType: "text/csv; charset=utf-8", Data: buf.Bytes()}, nil
}

// csvText neutralises a cell a spreadsheet would run as a formula. Memos and
// external ids come from callers, and finance opens these files in Excel.
func csvText(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

// period checks a report's [from, to).
func period(from, to *timestamppb.Timestamp) (time.Time, time.Time, error) {
	f, t := from.AsTime(), to.AsTime()
	if !t.After(f) {
		return f, t, apperrors.New(apperrors.InvalidArgument, "to must be after from")
	}
	return f, t, nil
}

// reportScope is the caller's scope, narrowed to one product when the
// request names one.
func (s *Service) reportScope(ctx context.Context, productID string) (*filter.ProductScope, error) {
	scope, err := s.callerScope(ctx)
	if err != nil {
		return nil, err
	}
	if productID == "" {
		return scope, nil
	}
	product, err := s.repo.GetProductByPublicID(ctx, productID)
	if err != nil {
		return nil, err
	}
	if err := requireProductInScope(scope, product.ID); err != nil {
		return nil, err
	}
	return filter.OnlyProducts(product.ID), nil
}
