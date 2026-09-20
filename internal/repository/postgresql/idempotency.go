package postgresql

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

const idempotencyColumns = `id, scope, idempotency_key, request_fingerprint, status,
	response_body, created_at, updated_at, expires_at`

// ClaimIdempotencyKey reserves a key, or reports who already holds it.
//
// INSERT ... ON CONFLICT DO NOTHING RETURNING is what makes this safe: the
// database decides the winner in one statement, so two concurrent identical
// requests cannot both believe they claimed the key. Doing this as SELECT then
// INSERT would leave a window where both see nothing and both proceed.
func (r *Repository) ClaimIdempotencyKey(ctx context.Context, key *dao.IdempotencyKey) (*dao.IdempotencyKey, bool, error) {
	const claim = `
		INSERT INTO idempotency_keys (scope, idempotency_key, request_fingerprint, status, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (scope, idempotency_key) DO NOTHING
		RETURNING ` + idempotencyColumns

	row := r.executor(ctx).QueryRowContext(ctx, claim,
		key.Scope, key.Key, key.RequestFingerprint, dao.IdempotencyInProgress, key.ExpiresAt)

	claimed, err := scanIdempotencyKey(row)
	if err == nil {
		return claimed, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, apperrors.Wrap(err, apperrors.Internal, "failed to claim idempotency key")
	}

	// Someone else holds it. Report what they left behind so the caller can
	// replay, reject or wait.
	const load = `SELECT ` + idempotencyColumns + `
		FROM idempotency_keys WHERE scope = $1 AND idempotency_key = $2`

	existing, err := scanIdempotencyKey(r.executor(ctx).QueryRowContext(ctx, load, key.Scope, key.Key))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The holder released or expired the key between our two statements.
			// Retrying the claim is the caller's business, not a silent loop here.
			return nil, false, apperrors.New(apperrors.IdempotencyInProgress,
				"idempotency key %q was released concurrently, retry the request", key.Key)
		}
		return nil, false, apperrors.Wrap(err, apperrors.Internal, "failed to load existing idempotency key")
	}
	return existing, false, nil
}

// CompleteIdempotencyKey records the response for a claimed key. Run it inside
// the transaction that did the work, so a recorded response always implies the
// work committed.
func (r *Repository) CompleteIdempotencyKey(ctx context.Context, scope, key string, response []byte) error {
	const query = `
		UPDATE idempotency_keys
		SET status = $1, response_body = $2
		WHERE scope = $3 AND idempotency_key = $4 AND status = $5`

	result, err := r.executor(ctx).ExecContext(ctx, query,
		dao.IdempotencyCompleted, response, scope, key, dao.IdempotencyInProgress)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to complete idempotency key")
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to read update result")
	}
	if affected == 0 {
		// The claim vanished or was already completed. Failing loudly beats
		// committing work whose response nobody can replay.
		return apperrors.New(apperrors.Internal,
			"idempotency key %q in scope %q was not in progress when completing", key, scope)
	}
	return nil
}

// ReleaseIdempotencyKey drops an in-progress claim so a retry can re-run the
// work. Completed keys are left alone: their response is still replayable.
func (r *Repository) ReleaseIdempotencyKey(ctx context.Context, scope, key string) error {
	const query = `DELETE FROM idempotency_keys
		WHERE scope = $1 AND idempotency_key = $2 AND status = $3`

	if _, err := r.executor(ctx).ExecContext(ctx, query, scope, key, dao.IdempotencyInProgress); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to release idempotency key")
	}
	return nil
}

// DeleteExpiredIdempotencyKeys prunes expired rows. This is also what frees a
// key abandoned by a process that crashed mid-work.
func (r *Repository) DeleteExpiredIdempotencyKeys(ctx context.Context, limit int) (int64, error) {
	const query = `
		DELETE FROM idempotency_keys
		WHERE id IN (
			SELECT id FROM idempotency_keys WHERE expires_at < NOW() ORDER BY expires_at LIMIT $1
		)`

	result, err := r.executor(ctx).ExecContext(ctx, query, limit)
	if err != nil {
		return 0, apperrors.Wrap(err, apperrors.Internal, "failed to delete expired idempotency keys")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, apperrors.Wrap(err, apperrors.Internal, "failed to read delete result")
	}
	return affected, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanIdempotencyKey(row rowScanner) (*dao.IdempotencyKey, error) {
	var (
		k    dao.IdempotencyKey
		body []byte
	)
	err := row.Scan(&k.ID, &k.Scope, &k.Key, &k.RequestFingerprint, &k.Status,
		&body, &k.CreatedAt, &k.UpdatedAt, &k.ExpiresAt)
	if err != nil {
		return nil, err
	}
	k.ResponseBody = body
	return &k, nil
}
