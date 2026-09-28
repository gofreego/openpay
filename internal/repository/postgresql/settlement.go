package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// CreateSettlement records a settlement. inserted is false when the provider
// sent this one before: ingesting a report twice must book the bank credit once.
func (r *Repository) CreateSettlement(ctx context.Context, s *dao.Settlement) (inserted bool, err error) {
	err = r.executor(ctx).QueryRowContext(ctx, `
		INSERT INTO settlements (public_id, provider, provider_settlement_id, bank, bank_reference, settled_at,
		                         gross, fees, fee_tax, net, currency, item_count, raw, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (provider, provider_settlement_id) DO NOTHING
		RETURNING id, created_at`,
		s.PublicID, s.Provider, s.ProviderSettlementID, s.Bank, s.BankReference, s.SettledAt,
		s.Gross, s.Fees, s.FeeTax, s.Net, s.Currency, s.ItemCount, s.Raw, s.Status,
	).Scan(&s.ID, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, apperrors.Wrap(err, apperrors.Internal, "failed to record settlement")
	}
	return true, nil
}

func (r *Repository) SetSettlementStatus(ctx context.Context, id int64, status dao.SettlementStatus) error {
	if _, err := r.executor(ctx).ExecContext(ctx, `UPDATE settlements SET status = $2 WHERE id = $1`, id, status); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to update settlement")
	}
	return nil
}

// LatestSettlementAt is the newest settlement already ingested from a
// provider, from which the next fetch continues.
func (r *Repository) LatestSettlementAt(ctx context.Context, providerName string) (time.Time, error) {
	var at sql.NullTime
	if err := r.executor(ctx).QueryRowContext(ctx,
		`SELECT MAX(settled_at) FROM settlements WHERE provider = $1`, providerName).Scan(&at); err != nil {
		return time.Time{}, apperrors.Wrap(err, apperrors.Internal, "failed to read latest settlement")
	}
	return at.Time, nil
}

const settlementColumns = `id, public_id, provider, provider_settlement_id, bank, bank_reference, settled_at,
	gross, fees, fee_tax, net, currency, item_count, raw, status, created_at`

func scanSettlement(row rowScanner) (*dao.Settlement, error) {
	var s dao.Settlement
	err := row.Scan(&s.ID, &s.PublicID, &s.Provider, &s.ProviderSettlementID, &s.Bank, &s.BankReference, &s.SettledAt,
		&s.Gross, &s.Fees, &s.FeeTax, &s.Net, &s.Currency, &s.ItemCount, &s.Raw, &s.Status, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) GetSettlementByPublicID(ctx context.Context, publicID string) (*dao.Settlement, error) {
	s, err := scanSettlement(r.executor(ctx).QueryRowContext(ctx,
		`SELECT `+settlementColumns+` FROM settlements WHERE public_id = $1`, publicID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "settlement %q not found", publicID)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load settlement")
	}
	return s, nil
}

func (r *Repository) ListSettlements(ctx context.Context, providerName string, limit int) ([]*dao.Settlement, error) {
	query := `SELECT ` + settlementColumns + ` FROM settlements`
	args := []any{}
	if providerName != "" {
		query += ` WHERE provider = $1`
		args = append(args, providerName)
	}
	args = append(args, limit)
	query += ` ORDER BY settled_at DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list settlements")
	}
	defer rows.Close()
	var out []*dao.Settlement
	for rows.Next() {
		s, err := scanSettlement(rows)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan settlement")
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repository) CreateSettlementItem(ctx context.Context, it *dao.SettlementItem) error {
	if err := r.executor(ctx).QueryRowContext(ctx, `
		INSERT INTO settlement_items (settlement_id, kind, provider_ref, gross, fee, fee_tax, net, expected, unexplained,
		                              payment_id, refund_id, dispute_id, product_id, classification)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`,
		it.SettlementID, it.Kind, it.ProviderRef, it.Gross, it.Fee, it.FeeTax, it.Net, it.Expected, it.Unexplained,
		it.PaymentID, it.RefundID, it.DisputeID, it.ProductID, it.Classification,
	).Scan(&it.ID); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to record settlement item")
	}
	return nil
}

// IsSettled reports whether a provider reference was already matched in an
// earlier settlement — a second appearance is a duplicate.
func (r *Repository) IsSettled(ctx context.Context, kind, providerRef string) (bool, error) {
	var n int
	if err := r.executor(ctx).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM settlement_items WHERE kind = $1 AND provider_ref = $2 AND classification = 'matched'`,
		kind, providerRef).Scan(&n); err != nil {
		return false, apperrors.Wrap(err, apperrors.Internal, "failed to check settlement item")
	}
	return n > 0, nil
}

func (r *Repository) ListSettlementItems(ctx context.Context, settlementID int64) ([]*dao.SettlementItem, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT id, settlement_id, kind, provider_ref, gross, fee, fee_tax, net, expected, unexplained,
		       payment_id, refund_id, dispute_id, product_id, classification
		FROM settlement_items WHERE settlement_id = $1 ORDER BY id`, settlementID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list settlement items")
	}
	defer rows.Close()
	var out []*dao.SettlementItem
	for rows.Next() {
		var it dao.SettlementItem
		if err := rows.Scan(&it.ID, &it.SettlementID, &it.Kind, &it.ProviderRef, &it.Gross, &it.Fee, &it.FeeTax, &it.Net,
			&it.Expected, &it.Unexplained, &it.PaymentID, &it.RefundID, &it.DisputeID, &it.ProductID, &it.Classification); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan settlement item")
		}
		out = append(out, &it)
	}
	return out, rows.Err()
}

// ---- Breaks ----

const breakColumns = `id, public_id, provider, classification, settlement_item_id, payment_id, product_id, amount,
	currency, detail, status, reason_code, note, resolved_by, resolved_at, created_at, updated_at`

// CreateBreak adds to the work queue. A payment already flagged missing at
// its provider is not flagged again (created false).
func (r *Repository) CreateBreak(ctx context.Context, b *dao.ReconBreak) (created bool, err error) {
	err = r.executor(ctx).QueryRowContext(ctx, `
		INSERT INTO recon_breaks (public_id, provider, classification, settlement_item_id, payment_id, product_id,
		                          amount, currency, detail, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT DO NOTHING
		RETURNING id, created_at, updated_at`,
		b.PublicID, b.Provider, b.Classification, b.SettlementItemID, b.PaymentID, b.ProductID,
		b.Amount, b.Currency, b.Detail, b.Status,
	).Scan(&b.ID, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, apperrors.Wrap(err, apperrors.Internal, "failed to record reconciliation break")
	}
	return true, nil
}

func (r *Repository) UpdateBreak(ctx context.Context, b *dao.ReconBreak) error {
	if err := r.executor(ctx).QueryRowContext(ctx, `
		UPDATE recon_breaks SET status = $2, reason_code = $3, note = $4, resolved_by = $5, resolved_at = $6
		WHERE id = $1 RETURNING updated_at`,
		b.ID, b.Status, b.ReasonCode, b.Note, b.ResolvedBy, b.ResolvedAt).Scan(&b.UpdatedAt); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to update reconciliation break")
	}
	return nil
}

func (r *Repository) LockBreak(ctx context.Context, publicID string) (*dao.ReconBreak, error) {
	if !InTx(ctx) {
		return nil, apperrors.New(apperrors.Internal, "LockBreak must run inside a transaction")
	}
	b, err := scanBreak(r.executor(ctx).QueryRowContext(ctx,
		`SELECT `+breakColumns+` FROM recon_breaks WHERE public_id = $1 FOR UPDATE`, publicID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "break %q not found", publicID)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load reconciliation break")
	}
	return b, nil
}

// OpenMissingAtProviderBreak finds a payment's open missing-at-provider break,
// so settling late can close it.
func (r *Repository) OpenMissingAtProviderBreak(ctx context.Context, paymentID int64) (*dao.ReconBreak, error) {
	b, err := scanBreak(r.executor(ctx).QueryRowContext(ctx, `SELECT `+breakColumns+` FROM recon_breaks
		WHERE payment_id = $1 AND classification = 'missing_at_provider' AND status = 'open' FOR UPDATE`, paymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load reconciliation break")
	}
	return b, nil
}

// ListBreaks is the work queue, oldest first, within scope. Platform-level
// breaks (no product) are visible only to the whole-estate scope.
func (r *Repository) ListBreaks(ctx context.Context, scope *filter.ProductScope, status dao.BreakStatus, limit int) ([]*dao.ReconBreak, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	scoped, err := scopeCondition(scope, "product_id", arg)
	if err != nil {
		return nil, err
	}
	if scoped != "" {
		where = append(where, scoped)
	}
	if status != "" {
		where = append(where, "status = "+arg(status))
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := r.executor(ctx).QueryContext(ctx, `SELECT `+breakColumns+` FROM recon_breaks`+clause+
		` ORDER BY created_at LIMIT `+arg(limit), args...)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list reconciliation breaks")
	}
	defer rows.Close()
	var out []*dao.ReconBreak
	for rows.Next() {
		b, err := scanBreak(rows)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan reconciliation break")
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BreakSummary counts open breaks, and those older than agedBefore.
func (r *Repository) BreakSummary(ctx context.Context, agedBefore time.Time) (open, aged int64, err error) {
	err = r.executor(ctx).QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE created_at < $1) FROM recon_breaks WHERE status = 'open'`,
		agedBefore).Scan(&open, &aged)
	if err != nil {
		return 0, 0, apperrors.Wrap(err, apperrors.Internal, "failed to summarise reconciliation breaks")
	}
	return open, aged, nil
}

// ListUnsettledPayments finds captured payments a provider has not settled
// since before a moment — candidates for "missing at provider".
func (r *Repository) ListUnsettledPayments(ctx context.Context, providerName string, capturedBefore time.Time, limit int) ([]*dao.Payment, error) {
	return r.queryPayments(ctx, `SELECT `+paymentColumns+paymentFrom+`
		WHERE p.provider = $1 AND p.captured_at IS NOT NULL AND p.captured_at < $2 AND p.settled_at IS NULL
		ORDER BY p.captured_at LIMIT $3`, providerName, capturedBefore, limit)
}

func scanBreak(row rowScanner) (*dao.ReconBreak, error) {
	var b dao.ReconBreak
	err := row.Scan(&b.ID, &b.PublicID, &b.Provider, &b.Classification, &b.SettlementItemID, &b.PaymentID, &b.ProductID,
		&b.Amount, &b.Currency, &b.Detail, &b.Status, &b.ReasonCode, &b.Note, &b.ResolvedBy, &b.ResolvedAt,
		&b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// SettlementByProduct splits one mixed bank credit by the product each line
// was matched to: of this credit, which product earned what. Unmatched lines
// form their own share with no product.
func (r *Repository) SettlementByProduct(ctx context.Context, settlementID int64) ([]dao.SettlementProductShare, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT i.product_id, p.public_id, COUNT(*), SUM(i.gross), SUM(i.fee), SUM(i.fee_tax), SUM(i.net)
		FROM settlement_items i LEFT JOIN products p ON p.id = i.product_id
		WHERE i.settlement_id = $1
		GROUP BY i.product_id, p.public_id
		ORDER BY i.product_id NULLS LAST`, settlementID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to split settlement by product")
	}
	defer rows.Close()
	var out []dao.SettlementProductShare
	for rows.Next() {
		var s dao.SettlementProductShare
		if err := rows.Scan(&s.ProductID, &s.ProductPublicID, &s.Lines, &s.Gross, &s.Fees, &s.FeeTax, &s.Net); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan product share")
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// FeeVariance compares fee recovery with actual PSP fees per product over a
// period (plan.md D12). Persistent one-sided variance means the rate card
// customers are charged from has drifted from what the provider charges.
func (r *Repository) FeeVariance(ctx context.Context, from, to time.Time) ([]dao.FeeVarianceLine, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT pr.id, pr.public_id,
		       COALESCE(SUM(-p.direction * p.amount) FILTER (WHERE a.code LIKE 'income:%:fee_recovery'), 0),
		       COALESCE(SUM(p.direction * p.amount)  FILTER (WHERE a.code LIKE 'expense:%:psp_fees'), 0)
		FROM products pr
		JOIN ledger_accounts a ON a.product_id = pr.id
		     AND (a.code LIKE 'income:%:fee_recovery' OR a.code LIKE 'expense:%:psp_fees')
		LEFT JOIN ledger_postings p ON p.account_id = a.id AND p.created_at >= $1 AND p.created_at < $2
		GROUP BY pr.id, pr.public_id
		ORDER BY pr.id`, from, to)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to compute fee variance")
	}
	defer rows.Close()
	var out []dao.FeeVarianceLine
	for rows.Next() {
		var l dao.FeeVarianceLine
		if err := rows.Scan(&l.ProductID, &l.ProductPublicID, &l.Charged, &l.Cost); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan fee variance")
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
