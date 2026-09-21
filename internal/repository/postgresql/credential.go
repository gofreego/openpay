package postgresql

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

const credentialColumns = `sc.id, sc.public_id, sc.product_id, sc.name, sc.key_id,
	sc.secret_hash, sc.status, sc.last_used_at, sc.revoked_at, sc.created_at, sc.updated_at`

func (r *Repository) CreateCredential(ctx context.Context, credential *dao.ServiceCredential) error {
	const query = `
		INSERT INTO service_credentials (public_id, product_id, name, key_id, secret_hash, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`

	err := r.executor(ctx).QueryRowContext(ctx, query,
		credential.PublicID, credential.ProductID, credential.Name,
		credential.KeyID, credential.SecretHash, credential.Status,
	).Scan(&credential.ID, &credential.CreatedAt, &credential.UpdatedAt)
	if err != nil {
		if isForeignKeyViolation(err) {
			return apperrors.Wrap(err, apperrors.NotFound, "product does not exist")
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to create service credential")
	}
	return nil
}

// GetCredentialByKeyID loads a credential together with its product.
//
// Both come back in one query because both matter on every authenticated call:
// an active credential belonging to a suspended product must not authenticate,
// and finding that out in a second round trip would double the cost of auth on
// the hot path.
func (r *Repository) GetCredentialByKeyID(ctx context.Context, keyID string) (*dao.ServiceCredential, *dao.Product, error) {
	const query = `
		SELECT ` + credentialColumns + `,
		       p.id, p.public_id, p.code, p.name, p.status, p.default_currency,
		       p.created_at, p.updated_at
		FROM service_credentials sc
		JOIN products p ON p.id = sc.product_id
		WHERE sc.key_id = $1`

	var (
		c dao.ServiceCredential
		p dao.Product
	)
	err := r.executor(ctx).QueryRowContext(ctx, query, keyID).Scan(
		&c.ID, &c.PublicID, &c.ProductID, &c.Name, &c.KeyID, &c.SecretHash,
		&c.Status, &c.LastUsedAt, &c.RevokedAt, &c.CreatedAt, &c.UpdatedAt,
		&p.ID, &p.PublicID, &p.Code, &p.Name, &p.Status, &p.DefaultCurrency,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, apperrors.New(apperrors.Unauthenticated, "invalid credentials")
		}
		return nil, nil, apperrors.Wrap(err, apperrors.Internal, "failed to load service credential")
	}
	return &c, &p, nil
}

func (r *Repository) ListCredentials(ctx context.Context, productID int64) ([]*dao.ServiceCredential, error) {
	const query = `SELECT ` + credentialColumns + `
		FROM service_credentials sc WHERE sc.product_id = $1 ORDER BY sc.id DESC`

	rows, err := r.executor(ctx).QueryContext(ctx, query, productID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list service credentials")
	}
	defer rows.Close()

	var credentials []*dao.ServiceCredential
	for rows.Next() {
		var c dao.ServiceCredential
		if err := rows.Scan(&c.ID, &c.PublicID, &c.ProductID, &c.Name, &c.KeyID,
			&c.SecretHash, &c.Status, &c.LastUsedAt, &c.RevokedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan service credential")
		}
		credentials = append(credentials, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate service credentials")
	}
	return credentials, nil
}

// RevokeCredential is idempotent: revoking an already-revoked credential
// succeeds, because the caller's intent is already satisfied and failing would
// only encourage them to retry something that cannot help.
func (r *Repository) RevokeCredential(ctx context.Context, publicID string) error {
	const query = `
		UPDATE service_credentials
		SET status = $1, revoked_at = COALESCE(revoked_at, NOW())
		WHERE public_id = $2`

	result, err := r.executor(ctx).ExecContext(ctx, query, dao.CredentialRevoked, publicID)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to revoke service credential")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to read update result")
	}
	if affected == 0 {
		return apperrors.New(apperrors.NotFound, "service credential %q not found", publicID)
	}
	return nil
}

// TouchCredentialUsed records last use. It is best effort by design: the answer
// it supports ("is anyone still using this key?") is worth having, but never at
// the cost of failing the request that produced it.
func (r *Repository) TouchCredentialUsed(ctx context.Context, id int64) error {
	const query = `UPDATE service_credentials SET last_used_at = NOW() WHERE id = $1`

	if _, err := r.executor(ctx).ExecContext(ctx, query, id); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to record credential use")
	}
	return nil
}
