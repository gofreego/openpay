package postgresql

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

const customerColumns = `id, public_id, external_ref, status, merged_into_customer_id,
	created_at, updated_at`

// mergeHops bounds how far a merge chain is followed. A cycle is prevented by a
// CHECK constraint and by always merging into a non-merged survivor, but a
// bounded loop beats trusting that forever: an infinite loop here would hang
// every request for that customer.
const mergeHops = 8

// UpsertCustomer creates a customer or returns the existing one for the same
// external_ref.
//
// ON CONFLICT DO UPDATE rather than DO NOTHING, because DO NOTHING returns no
// row on conflict and would need a second query. The update is a no-op write of
// the value already there, which is enough to make RETURNING produce the row.
func (r *Repository) UpsertCustomer(ctx context.Context, customer *dao.Customer) error {
	const query = `
		INSERT INTO customers (public_id, external_ref, status)
		VALUES ($1, $2, $3)
		ON CONFLICT (external_ref) DO UPDATE SET external_ref = EXCLUDED.external_ref
		RETURNING ` + customerColumns

	existing, err := scanCustomer(r.executor(ctx).QueryRowContext(ctx, query,
		customer.PublicID, customer.ExternalRef, dao.CustomerActive))
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to upsert customer")
	}

	*customer = *existing

	// A merged record is a tombstone; the caller wants whoever it points at.
	if customer.IsMerged() {
		survivor, err := r.resolveMerge(ctx, customer)
		if err != nil {
			return err
		}
		*customer = *survivor
	}
	return nil
}

// GetCustomerByExternalRef looks a person up by their OpenAuth user id,
// following a merge to the surviving record.
func (r *Repository) GetCustomerByExternalRef(ctx context.Context, externalRef string) (*dao.Customer, error) {
	const query = `SELECT ` + customerColumns + ` FROM customers WHERE external_ref = $1`

	customer, err := scanCustomer(r.executor(ctx).QueryRowContext(ctx, query, externalRef))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperrors.New(apperrors.NotFound, "customer %q not found", externalRef)
		}
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load customer")
	}
	if customer.IsMerged() {
		return r.resolveMerge(ctx, customer)
	}
	return customer, nil
}

// GetCustomerByPublicID looks a customer up by id, following a merge.
func (r *Repository) GetCustomerByPublicID(ctx context.Context, publicID string) (*dao.Customer, error) {
	const query = `SELECT ` + customerColumns + ` FROM customers WHERE public_id = $1`

	customer, err := scanCustomer(r.executor(ctx).QueryRowContext(ctx, query, publicID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperrors.New(apperrors.NotFound, "customer %q not found", publicID)
		}
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load customer")
	}
	if customer.IsMerged() {
		return r.resolveMerge(ctx, customer)
	}
	return customer, nil
}

// resolveMerge walks from a tombstone to the surviving record, so an id that a
// product's backend stored before a merge keeps working rather than 404ing.
func (r *Repository) resolveMerge(ctx context.Context, customer *dao.Customer) (*dao.Customer, error) {
	const query = `SELECT ` + customerColumns + ` FROM customers WHERE id = $1`

	current := customer
	for range mergeHops {
		if !current.IsMerged() {
			return current, nil
		}
		next, err := scanCustomer(r.executor(ctx).QueryRowContext(ctx, query, *current.MergedIntoCustomerID))
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal,
				"failed to resolve merged customer %q", current.PublicID)
		}
		current = next
	}
	return nil, apperrors.New(apperrors.Internal,
		"customer %q merge chain is longer than %d hops, refusing to follow further",
		customer.PublicID, mergeHops)
}

func scanCustomer(row rowScanner) (*dao.Customer, error) {
	var c dao.Customer
	err := row.Scan(&c.ID, &c.PublicID, &c.ExternalRef, &c.Status,
		&c.MergedIntoCustomerID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}
