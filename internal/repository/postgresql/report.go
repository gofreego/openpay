package postgresql

import (
	"context"
	"strconv"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// topFailures is how many failure codes each provider line carries.
const topFailures = 5

// ProductPnL returns every product income and expense account's movement in
// [from, to), including accounts that did not move, so a product with a quiet
// period still shows its lines at zero rather than vanishing.
func (r *Repository) ProductPnL(ctx context.Context, scope *filter.ProductScope, from, to time.Time) ([]*dao.PnLLine, error) {
	args := []any{from, to}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	scoped, err := scopeCondition(scope, "a.product_id", arg)
	if err != nil {
		return nil, err
	}
	where := ""
	if scoped != "" {
		where = " AND " + scoped
	}

	// direction is +1 for a debit, -1 for a credit. Income grows by credits,
	// expense by debits, so each is flipped into its natural direction.
	query := `
		SELECT pr.id, pr.public_id, pr.code, a.currency, a.code, a.type,
		       COALESCE(SUM(p.direction * p.amount), 0) * CASE WHEN a.type = 'income' THEN -1 ELSE 1 END
		FROM ledger_accounts a
		JOIN products pr ON pr.id = a.product_id
		LEFT JOIN ledger_postings p ON p.account_id = a.id AND p.created_at >= $1 AND p.created_at < $2
		WHERE a.type IN ('income', 'expense')` + where + `
		GROUP BY pr.id, pr.public_id, pr.code, a.currency, a.code, a.type
		ORDER BY pr.id, a.currency, a.type DESC, a.code`

	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to compute product P&L")
	}
	defer rows.Close()
	var out []*dao.PnLLine
	for rows.Next() {
		var l dao.PnLLine
		if err := rows.Scan(&l.ProductID, &l.ProductPublicID, &l.ProductCode, &l.Currency,
			&l.AccountCode, &l.Type, &l.Amount); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan P&L line")
		}
		out = append(out, &l)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate P&L")
	}
	return out, nil
}

// ProviderStats counts payment attempts started in [from, to) per provider,
// by their current status, with each provider's commonest failure codes.
func (r *Repository) ProviderStats(ctx context.Context, scope *filter.ProductScope, from, to time.Time) ([]*dao.ProviderStats, error) {
	args := []any{from, to}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	scoped, err := scopeCondition(scope, "pay.product_id", arg)
	if err != nil {
		return nil, err
	}
	where := ""
	if scoped != "" {
		where = " AND " + scoped
	}
	base := `
		FROM payment_attempts a
		JOIN payments pay ON pay.id = a.payment_id
		WHERE a.created_at >= $1 AND a.created_at < $2` + where

	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT a.provider, COUNT(*),
		       COUNT(*) FILTER (WHERE a.status = 'captured'),
		       COUNT(*) FILTER (WHERE a.status = 'failed'),
		       COUNT(*) FILTER (WHERE a.status = 'expired'),
		       COUNT(*) FILTER (WHERE a.status = 'cancelled'),
		       COUNT(*) FILTER (WHERE a.status IN ('created', 'pending', 'authorized')),
		       COALESCE(SUM(pay.captured_amount) FILTER (WHERE a.status = 'captured'), 0)`+base+`
		GROUP BY a.provider
		ORDER BY a.provider`, args...)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to count provider attempts")
	}
	defer rows.Close()
	var out []*dao.ProviderStats
	byProvider := map[string]*dao.ProviderStats{}
	for rows.Next() {
		var s dao.ProviderStats
		if err := rows.Scan(&s.Provider, &s.Attempts, &s.Captured, &s.Failed, &s.Expired,
			&s.Cancelled, &s.Open, &s.CapturedAmount); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan provider stats")
		}
		out = append(out, &s)
		byProvider[s.Provider] = &s
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate provider stats")
	}

	failures, err := r.executor(ctx).QueryContext(ctx, `
		SELECT a.provider, COALESCE(NULLIF(a.failure_code, ''), 'unknown'), COUNT(*)`+base+`
		  AND a.status = 'failed'
		GROUP BY 1, 2
		ORDER BY 1, 3 DESC, 2`, args...)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to count provider failures")
	}
	defer failures.Close()
	for failures.Next() {
		var (
			provider string
			f        dao.FailureCount
		)
		if err := failures.Scan(&provider, &f.Code, &f.Count); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan provider failure")
		}
		if s := byProvider[provider]; s != nil && len(s.TopFailures) < topFailures {
			s.TopFailures = append(s.TopFailures, f)
		}
	}
	if err := failures.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate provider failures")
	}
	return out, nil
}

// ListStatementRange reads an account's postings in [from, to), oldest first,
// starting after afterPostingID (0 for the start of the period).
func (r *Repository) ListStatementRange(ctx context.Context, accountID int64, from, to time.Time, afterPostingID int64, limit int) ([]*dao.StatementEntry, error) {
	const query = `
		SELECT p.id, p.journal_id, p.account_id, p.direction, p.amount, p.currency,
		       p.seq, p.balance_after, p.created_at,
		       j.public_id, j.external_id, j.kind, COALESCE(j.memo, ''), j.posted_at
		FROM ledger_postings p
		JOIN ledger_journals j ON j.id = p.journal_id
		WHERE p.account_id = $1 AND p.created_at >= $2 AND p.created_at < $3 AND p.id > $4
		ORDER BY p.id
		LIMIT $5`

	rows, err := r.executor(ctx).QueryContext(ctx, query, accountID, from, to, afterPostingID, limit)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load statement range")
	}
	defer rows.Close()

	var entries []*dao.StatementEntry
	for rows.Next() {
		var (
			e dao.StatementEntry
			p dao.Posting
		)
		if err := rows.Scan(&p.ID, &p.JournalID, &p.AccountID, &p.Direction, &p.Amount, &p.Currency,
			&p.Seq, &p.BalanceAfter, &p.CreatedAt,
			&e.JournalPublicID, &e.JournalExternalID, &e.JournalKind, &e.JournalMemo, &e.JournalPostedAt); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan statement entry")
		}
		e.Posting = &p
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate statement range")
	}
	return entries, nil
}
