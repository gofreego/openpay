package postgresql

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"context"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
)

const productColumns = `id, public_id, code, name, status, default_currency, created_at, updated_at`

func (r *Repository) CreateProduct(ctx context.Context, product *dao.Product) error {
	const query = `
		INSERT INTO products (public_id, code, name, status, default_currency)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at`

	err := r.executor(ctx).QueryRowContext(ctx, query,
		product.PublicID, product.Code, product.Name, product.Status, product.DefaultCurrency,
	).Scan(&product.ID, &product.CreatedAt, &product.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return apperrors.Wrap(err, apperrors.AlreadyExists, "product code %q is already taken", product.Code)
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to create product")
	}
	return nil
}

func (r *Repository) GetProductByPublicID(ctx context.Context, publicID string) (*dao.Product, error) {
	const query = `SELECT ` + productColumns + ` FROM products WHERE public_id = $1`
	return r.getProduct(ctx, query, publicID, "product %q not found", publicID)
}

func (r *Repository) GetProductByCode(ctx context.Context, code string) (*dao.Product, error) {
	const query = `SELECT ` + productColumns + ` FROM products WHERE code = $1`
	return r.getProduct(ctx, query, code, "product with code %q not found", code)
}

func (r *Repository) getProduct(ctx context.Context, query, arg, notFoundMsg string, notFoundArgs ...any) (*dao.Product, error) {
	product, err := scanProduct(r.executor(ctx).QueryRowContext(ctx, query, arg))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperrors.New(apperrors.NotFound, notFoundMsg, notFoundArgs...)
		}
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load product")
	}
	return product, nil
}

func (r *Repository) ListProducts(ctx context.Context, f *filter.Product) ([]*dao.Product, int64, error) {
	f.WithDefaults()

	// Built once and used for both the page and the count, so the total always
	// describes the same set the page came from.
	var (
		where []string
		args  []any
	)
	if f.Search != "" {
		args = append(args, "%"+strings.ToLower(f.Search)+"%")
		where = append(where, "(LOWER(code) LIKE $1 OR LOWER(name) LIKE $1)")
	}
	if f.Status != "" {
		args = append(args, f.Status)
		where = append(where, "status = $"+strconv.Itoa(len(args)))
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	if err := r.executor(ctx).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM products`+clause, args...).Scan(&total); err != nil {
		return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to count products")
	}

	args = append(args, f.Limit, f.Offset)
	query := `SELECT ` + productColumns + ` FROM products` + clause +
		` ORDER BY id DESC LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))

	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to list products")
	}
	defer rows.Close()

	var products []*dao.Product
	for rows.Next() {
		product, err := scanProduct(rows)
		if err != nil {
			return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to scan product")
		}
		products = append(products, product)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to iterate products")
	}
	return products, total, nil
}

// UpdateProduct changes the mutable fields only. Code and default currency are
// absent on purpose: ledger account codes already embed the code, and changing
// the currency of a product with balances behind it would make its history
// unreadable.
func (r *Repository) UpdateProduct(ctx context.Context, product *dao.Product) error {
	const query = `
		UPDATE products SET name = $1, status = $2
		WHERE public_id = $3
		RETURNING id, code, default_currency, created_at, updated_at`

	err := r.executor(ctx).QueryRowContext(ctx, query,
		product.Name, product.Status, product.PublicID,
	).Scan(&product.ID, &product.Code, &product.DefaultCurrency, &product.CreatedAt, &product.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperrors.New(apperrors.NotFound, "product %q not found", product.PublicID)
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to update product")
	}
	return nil
}

func scanProduct(row rowScanner) (*dao.Product, error) {
	var p dao.Product
	err := row.Scan(&p.ID, &p.PublicID, &p.Code, &p.Name, &p.Status,
		&p.DefaultCurrency, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}
