package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

const holdColumns = `id, public_id, external_id, account_id, amount, currency, status,
	expires_at, capture_journal_id, captured_amount, resolved_at, created_at, updated_at`

// Lock order for holds: the hold row first, then balance rows in ascending
// account id. Placing a hold inserts its row before locking the balance, and
// capturing or releasing one locks the row before the balances, so every path
// agrees and none can deadlock against another.

// PlaceHold reserves part of an account's available balance.
//
// It is idempotent on ExternalID: placing the same hold again returns the one
// already placed, provided it is the same request. The same external id for a
// different account or amount is a conflict, not a replay — silently returning
// the first hold would reserve the wrong money.
func (r *Repository) PlaceHold(ctx context.Context, hold *dao.Hold) error {
	if !InTx(ctx) {
		return apperrors.New(apperrors.Internal,
			"PlaceHold must run inside a transaction: the hold and the reservation must commit together")
	}
	if hold.ExternalID == "" {
		return apperrors.New(apperrors.Internal, "a hold must carry an external id to be idempotent")
	}
	if hold.Amount <= 0 {
		return apperrors.New(apperrors.InvalidArgument, "a hold must be for a positive amount, got %d", hold.Amount)
	}
	if !hold.ExpiresAt.After(time.Now()) {
		return apperrors.New(apperrors.InvalidArgument, "a hold must expire in the future")
	}

	const insert = `
		INSERT INTO ledger_holds (public_id, external_id, account_id, amount, currency, status, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (external_id) DO NOTHING
		RETURNING id, status, created_at, updated_at`

	err := r.executor(ctx).QueryRowContext(ctx, insert,
		hold.PublicID, hold.ExternalID, hold.AccountID, hold.Amount, hold.Currency,
		dao.HoldActive, hold.ExpiresAt,
	).Scan(&hold.ID, &hold.Status, &hold.CreatedAt, &hold.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r.replayHold(ctx, hold)
	}
	if err != nil {
		if isForeignKeyViolation(err) {
			return apperrors.Wrap(err, apperrors.NotFound, "ledger account %d does not exist", hold.AccountID)
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to place hold")
	}

	account, err := r.lockAccount(ctx, hold.AccountID)
	if err != nil {
		return err
	}
	if account.currency != hold.Currency {
		return apperrors.New(apperrors.CurrencyMismatch,
			"a hold in %s cannot be placed on account %q, which is denominated in %s",
			hold.Currency, account.code, account.currency)
	}

	// A hold on an account that may go negative reserves nothing it could run
	// out of, so only the others are checked.
	natural := account.rawBalance * account.accountType.NormalSign()
	if available := natural - account.held; !account.allowNegative && available < hold.Amount {
		return apperrors.New(apperrors.InsufficientBalance,
			"account %q has %d available (balance %d, held %d), cannot hold %d",
			account.code, available, natural, account.held, hold.Amount)
	}

	const reserve = `
		UPDATE ledger_balances
		SET held = held + $2, version = version + 1, updated_at = NOW()
		WHERE account_id = $1`
	if _, err := r.executor(ctx).ExecContext(ctx, reserve, account.id, hold.Amount); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to reserve %d on %q", hold.Amount, account.code)
	}
	return nil
}

// replayHold answers a repeated PlaceHold with the hold already placed.
func (r *Repository) replayHold(ctx context.Context, hold *dao.Hold) error {
	existing, err := r.GetHoldByExternalID(ctx, hold.ExternalID)
	if err != nil {
		return err
	}
	if existing.AccountID != hold.AccountID || existing.Amount != hold.Amount || existing.Currency != hold.Currency {
		return apperrors.New(apperrors.AlreadyExists,
			"hold %q already exists for a different account or amount", hold.ExternalID)
	}
	*hold = *existing
	return nil
}

// CaptureHold turns a hold into a real movement of money by posting journal,
// and frees the whole reservation in the same step.
//
// The journal must take between 1 and the hold's amount out of the held
// account. Capturing less than was held is ordinary — a checkout that held
// ₹300 and cost ₹250 — and the remainder simply stops being reserved.
//
// Capturing is idempotent on the journal's external id: a retry with the same
// journal returns the capture already made. Capturing with a different
// journal, or capturing a hold that was released, is refused.
func (r *Repository) CaptureHold(ctx context.Context, holdExternalID string, journal *dao.Journal) (*dao.Hold, error) {
	if !InTx(ctx) {
		return nil, apperrors.New(apperrors.Internal,
			"CaptureHold must run inside a transaction: the capture journal and the hold's release must commit together")
	}

	hold, err := r.lockHold(ctx, holdExternalID)
	if err != nil {
		return nil, err
	}

	if hold.Status == dao.HoldCaptured {
		captured, err := r.GetJournalByExternalID(ctx, journal.ExternalID)
		if err == nil && hold.CaptureJournalID != nil && captured.ID == *hold.CaptureJournalID {
			*journal = *captured
			return hold, nil
		}
		return nil, apperrors.New(apperrors.FailedPrecondition,
			"hold %q was already captured by a different journal", holdExternalID)
	}
	if hold.Status != dao.HoldActive {
		return nil, apperrors.New(apperrors.FailedPrecondition,
			"hold %q is %s and can no longer be captured", holdExternalID, hold.Status)
	}

	account, err := r.getLedgerAccountByID(ctx, hold.AccountID)
	if err != nil {
		return nil, err
	}
	captured := -netNaturalChange(journal, account)
	if captured <= 0 || captured > hold.Amount {
		return nil, apperrors.New(apperrors.InvalidArgument,
			"capturing hold %q must take between 1 and %d from %q, the journal takes %d",
			holdExternalID, hold.Amount, account.Code, captured)
	}

	posted, err := r.postJournal(ctx, journal, &holdRelease{accountID: hold.AccountID, amount: hold.Amount})
	if err != nil {
		return nil, err
	}
	if !posted {
		// The journal already exists but the hold is still active, so something
		// else posted it. Releasing the hold against it would double-count.
		return nil, apperrors.New(apperrors.AlreadyExists,
			"journal %q was already posted outside this hold's capture", journal.ExternalID)
	}

	const capture = `
		UPDATE ledger_holds
		SET status = $2, capture_journal_id = $3, captured_amount = $4, resolved_at = NOW()
		WHERE id = $1
		RETURNING ` + holdColumns

	return scanHold(r.executor(ctx).QueryRowContext(ctx, capture, hold.ID, dao.HoldCaptured, journal.ID, captured))
}

// ReleaseHold frees a hold without moving any money: the checkout failed, was
// cancelled, or was abandoned. Releasing a hold that is already released is a
// no-op; releasing a captured one is refused, since that money has moved.
func (r *Repository) ReleaseHold(ctx context.Context, holdExternalID string) (*dao.Hold, error) {
	return r.resolveHold(ctx, holdExternalID, dao.HoldReleased)
}

// ExpireHold is ReleaseHold for the sweeper, recording that nobody decided —
// the hold simply ran out of time. It refuses a hold not yet past expiry.
func (r *Repository) ExpireHold(ctx context.Context, holdExternalID string) (*dao.Hold, error) {
	return r.resolveHold(ctx, holdExternalID, dao.HoldExpired)
}

func (r *Repository) resolveHold(ctx context.Context, holdExternalID string, to dao.HoldStatus) (*dao.Hold, error) {
	if !InTx(ctx) {
		return nil, apperrors.New(apperrors.Internal,
			"releasing a hold must run inside a transaction: its status and the reservation must change together")
	}

	hold, err := r.lockHold(ctx, holdExternalID)
	if err != nil {
		return nil, err
	}

	switch {
	case hold.Status == to:
		return hold, nil
	case hold.Status != dao.HoldActive:
		return nil, apperrors.New(apperrors.FailedPrecondition,
			"hold %q is already %s and cannot become %s", holdExternalID, hold.Status, to)
	case to == dao.HoldExpired && time.Now().Before(hold.ExpiresAt):
		return nil, apperrors.New(apperrors.FailedPrecondition,
			"hold %q does not expire until %s", holdExternalID, hold.ExpiresAt.Format(time.RFC3339))
	}

	account, err := r.lockAccount(ctx, hold.AccountID)
	if err != nil {
		return nil, err
	}
	if account.held < hold.Amount {
		return nil, apperrors.New(apperrors.Internal,
			"account %q holds %d but a hold of %d is being released", account.code, account.held, hold.Amount)
	}

	const unreserve = `
		UPDATE ledger_balances
		SET held = held - $2, version = version + 1, updated_at = NOW()
		WHERE account_id = $1`
	if _, err := r.executor(ctx).ExecContext(ctx, unreserve, account.id, hold.Amount); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to release %d on %q", hold.Amount, account.code)
	}

	const resolve = `
		UPDATE ledger_holds SET status = $2, resolved_at = NOW()
		WHERE id = $1
		RETURNING ` + holdColumns
	return scanHold(r.executor(ctx).QueryRowContext(ctx, resolve, hold.ID, to))
}

// ListExpiredHolds returns the external ids of active holds past their expiry,
// oldest first, for the sweeper to expire one transaction at a time.
func (r *Repository) ListExpiredHolds(ctx context.Context, now time.Time, limit int) ([]string, error) {
	const query = `
		SELECT external_id FROM ledger_holds
		WHERE status = 'active' AND expires_at <= $1
		ORDER BY expires_at
		LIMIT $2`

	rows, err := r.executor(ctx).QueryContext(ctx, query, now, limit)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list expired holds")
	}
	defer rows.Close()

	var externalIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan expired hold")
		}
		externalIDs = append(externalIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate expired holds")
	}
	return externalIDs, nil
}

func (r *Repository) GetHoldByExternalID(ctx context.Context, externalID string) (*dao.Hold, error) {
	const query = `SELECT ` + holdColumns + ` FROM ledger_holds WHERE external_id = $1`
	return r.queryHold(ctx, query, externalID)
}

func (r *Repository) lockHold(ctx context.Context, externalID string) (*dao.Hold, error) {
	const query = `SELECT ` + holdColumns + ` FROM ledger_holds WHERE external_id = $1 FOR UPDATE`
	return r.queryHold(ctx, query, externalID)
}

func (r *Repository) queryHold(ctx context.Context, query, externalID string) (*dao.Hold, error) {
	hold, err := scanHold(r.executor(ctx).QueryRowContext(ctx, query, externalID))
	if err != nil {
		if apperrors.Is(err, apperrors.NotFound) {
			return nil, apperrors.New(apperrors.NotFound, "hold %q not found", externalID)
		}
		return nil, err
	}
	return hold, nil
}

// lockAccount locks one account's balance row, for operations that touch a
// single account and so have no ordering to get wrong.
func (r *Repository) lockAccount(ctx context.Context, accountID int64) (*lockedAccount, error) {
	const query = `
		SELECT a.id, a.code, a.type, a.currency, a.allow_negative, a.status,
		       b.raw_balance, b.held
		FROM ledger_accounts a
		JOIN ledger_balances b ON b.account_id = a.id
		WHERE a.id = $1
		FOR UPDATE OF b`

	var a lockedAccount
	err := r.executor(ctx).QueryRowContext(ctx, query, accountID).Scan(&a.id, &a.code, &a.accountType,
		&a.currency, &a.allowNegative, &a.status, &a.rawBalance, &a.held)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperrors.New(apperrors.NotFound, "ledger account %d does not exist", accountID)
		}
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to lock ledger account %d", accountID)
	}
	if a.status != dao.AccountActive {
		return nil, apperrors.New(apperrors.FailedPrecondition,
			"ledger account %q is closed", a.code)
	}
	return &a, nil
}

// netNaturalChange is how much a journal moves an account's natural balance.
func netNaturalChange(journal *dao.Journal, account *dao.LedgerAccount) int64 {
	var raw int64
	for _, posting := range journal.Postings {
		if posting.AccountID == account.ID {
			raw += posting.Signed()
		}
	}
	return raw * account.Type.NormalSign()
}

func scanHold(row rowScanner) (*dao.Hold, error) {
	var h dao.Hold
	err := row.Scan(&h.ID, &h.PublicID, &h.ExternalID, &h.AccountID, &h.Amount, &h.Currency,
		&h.Status, &h.ExpiresAt, &h.CaptureJournalID, &h.CapturedAmount, &h.ResolvedAt,
		&h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperrors.New(apperrors.NotFound, "hold not found")
		}
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load hold")
	}
	return &h, nil
}
