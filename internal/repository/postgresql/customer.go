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
// external_ref, reporting which happened.
//
// ON CONFLICT DO NOTHING, then a lookup — not DO UPDATE. A no-op update would
// return the row in one round trip, but it still fires the updated_at trigger,
// so every repeat lookup would bump the timestamp and updated_at would come to
// mean "last time anyone asked about this person" rather than "last time they
// changed". Every transacting user passes through here, so that is also a write
// on a read-shaped path.
func (r *Repository) UpsertCustomer(ctx context.Context, customer *dao.Customer) (created bool, err error) {
	const insert = `
		INSERT INTO customers (public_id, external_ref, status)
		VALUES ($1, $2, $3)
		ON CONFLICT (external_ref) DO NOTHING
		RETURNING ` + customerColumns

	inserted, err := scanCustomer(r.executor(ctx).QueryRowContext(ctx, insert,
		customer.PublicID, customer.ExternalRef, dao.CustomerActive))
	if err == nil {
		*customer = *inserted
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, apperrors.Wrap(err, apperrors.Internal, "failed to create customer")
	}

	existing, err := r.GetCustomerByExternalRef(ctx, customer.ExternalRef)
	if err != nil {
		return false, err
	}
	*customer = *existing
	return false, nil
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
