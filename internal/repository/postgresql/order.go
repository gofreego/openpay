package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// ---- Items ----

const itemColumns = `id, public_id, product_id, code, name, reference_price, currency, tax_class, status, created_at, updated_at`

func (r *Repository) CreateItem(ctx context.Context, item *dao.Item) error {
	err := r.executor(ctx).QueryRowContext(ctx, `
		INSERT INTO items (public_id, product_id, code, name, reference_price, currency, tax_class, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at`,
		item.PublicID, item.ProductID, item.Code, item.Name, item.ReferencePrice, item.Currency, item.TaxClass, item.Status,
	).Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return apperrors.Wrap(err, apperrors.AlreadyExists, "item %q already exists for this product", item.Code)
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to create item")
	}
	return nil
}

func (r *Repository) GetItemByPublicID(ctx context.Context, publicID string) (*dao.Item, error) {
	var it dao.Item
	err := r.executor(ctx).QueryRowContext(ctx, `SELECT `+itemColumns+` FROM items WHERE public_id = $1`, publicID).
		Scan(&it.ID, &it.PublicID, &it.ProductID, &it.Code, &it.Name, &it.ReferencePrice, &it.Currency, &it.TaxClass,
			&it.Status, &it.CreatedAt, &it.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "item %q not found", publicID)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load item")
	}
	return &it, nil
}

func (r *Repository) ListItems(ctx context.Context, productID int64) ([]*dao.Item, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `SELECT `+itemColumns+` FROM items WHERE product_id = $1 ORDER BY code`, productID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list items")
	}
	defer rows.Close()
	var items []*dao.Item
	for rows.Next() {
		var it dao.Item
		if err := rows.Scan(&it.ID, &it.PublicID, &it.ProductID, &it.Code, &it.Name, &it.ReferencePrice, &it.Currency,
			&it.TaxClass, &it.Status, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan item")
		}
		items = append(items, &it)
	}
	return items, rows.Err()
}

// ---- Orders ----

const orderColumns = `o.id, o.public_id, o.product_id, o.customer_id, o.external_ref, o.invoice_ref, o.currency,
	o.subtotal, o.discount, o.tax, o.total, o.tax_rate, o.tax_breakdown_provided, o.status, o.failure_reason,
	o.gateway_amount, o.expires_at, o.paid_at, pr.public_id, c.public_id, o.created_at, o.updated_at`

const orderFrom = ` FROM orders o JOIN products pr ON pr.id = o.product_id LEFT JOIN customers c ON c.id = o.customer_id`

func (r *Repository) CreateOrder(ctx context.Context, o *dao.Order) error {
	err := r.executor(ctx).QueryRowContext(ctx, `
		INSERT INTO orders (public_id, product_id, customer_id, external_ref, invoice_ref, currency, subtotal, discount,
		                    tax, total, tax_rate, tax_breakdown_provided, status, gateway_amount, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		RETURNING id, created_at, updated_at`,
		o.PublicID, o.ProductID, o.CustomerID, o.ExternalRef, o.InvoiceRef, o.Currency, o.Subtotal, o.Discount,
		o.Tax, o.Total, o.TaxRate, o.TaxBreakdownProvided, o.Status, o.GatewayAmount, o.ExpiresAt,
	).Scan(&o.ID, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return apperrors.Wrap(err, apperrors.AlreadyExists, "order %q already exists for this product", o.ExternalRef)
		}
		if isCheckViolation(err, "ck_orders_arithmetic") {
			return apperrors.Wrap(err, apperrors.InvalidArgument, "subtotal - discount + tax must equal total")
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to create order")
	}
	return nil
}

func (r *Repository) UpdateOrder(ctx context.Context, o *dao.Order) error {
	err := r.executor(ctx).QueryRowContext(ctx, `
		UPDATE orders SET status = $2, failure_reason = $3, paid_at = $4 WHERE id = $1 RETURNING updated_at`,
		o.ID, o.Status, o.FailureReason, o.PaidAt).Scan(&o.UpdatedAt)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to update order %s", o.PublicID)
	}
	return nil
}

func (r *Repository) GetOrderByPublicID(ctx context.Context, publicID string) (*dao.Order, error) {
	return r.queryOrder(ctx, `SELECT `+orderColumns+orderFrom+` WHERE o.public_id = $1`, publicID)
}

func (r *Repository) GetOrderByExternalRef(ctx context.Context, productID int64, externalRef string) (*dao.Order, error) {
	o, err := scanOrder(r.executor(ctx).QueryRowContext(ctx,
		`SELECT `+orderColumns+orderFrom+` WHERE o.product_id = $1 AND o.external_ref = $2`, productID, externalRef))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "order %q not found", externalRef)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load order")
	}
	return o, nil
}

// LockOrder holds an order's row until the transaction ends, so a capture
// and a failure cannot both finish it.
func (r *Repository) LockOrder(ctx context.Context, id int64) (*dao.Order, error) {
	if !InTx(ctx) {
		return nil, apperrors.New(apperrors.Internal, "LockOrder must run inside a transaction")
	}
	return r.queryOrder(ctx, `SELECT `+orderColumns+orderFrom+` WHERE o.id = $1 FOR UPDATE OF o`, id)
}

func (r *Repository) queryOrder(ctx context.Context, query string, arg any) (*dao.Order, error) {
	o, err := scanOrder(r.executor(ctx).QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "order %v not found", arg)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load order")
	}
	return o, nil
}

// ListExpiredOrders finds orders still awaiting payment past their expiry.
func (r *Repository) ListExpiredOrders(ctx context.Context, now time.Time, limit int) ([]*dao.Order, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `SELECT `+orderColumns+orderFrom+`
		WHERE o.status = 'pending_payment' AND o.expires_at <= $1 ORDER BY o.expires_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list expired orders")
	}
	defer rows.Close()
	var orders []*dao.Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan order")
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

func scanOrder(row rowScanner) (*dao.Order, error) {
	var o dao.Order
	err := row.Scan(&o.ID, &o.PublicID, &o.ProductID, &o.CustomerID, &o.ExternalRef, &o.InvoiceRef, &o.Currency,
		&o.Subtotal, &o.Discount, &o.Tax, &o.Total, &o.TaxRate, &o.TaxBreakdownProvided, &o.Status, &o.FailureReason,
		&o.GatewayAmount, &o.ExpiresAt, &o.PaidAt, &o.ProductPublicID, &o.CustomerPublicID, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// ---- Lines and tenders ----

func (r *Repository) CreateOrderLine(ctx context.Context, l *dao.OrderLine) error {
	err := r.executor(ctx).QueryRowContext(ctx, `
		INSERT INTO order_line_items (order_id, item_id, description, quantity, unit_amount, amount)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		l.OrderID, l.ItemID, l.Description, l.Quantity, l.UnitAmount, l.Amount).Scan(&l.ID)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to create order line")
	}
	return nil
}

func (r *Repository) ListOrderLines(ctx context.Context, orderID int64) ([]*dao.OrderLine, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT l.id, l.order_id, l.item_id, l.description, l.quantity, l.unit_amount, l.amount, i.public_id
		FROM order_line_items l LEFT JOIN items i ON i.id = l.item_id
		WHERE l.order_id = $1 ORDER BY l.id`, orderID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list order lines")
	}
	defer rows.Close()
	var lines []*dao.OrderLine
	for rows.Next() {
		var l dao.OrderLine
		if err := rows.Scan(&l.ID, &l.OrderID, &l.ItemID, &l.Description, &l.Quantity, &l.UnitAmount, &l.Amount, &l.ItemPublicID); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan order line")
		}
		lines = append(lines, &l)
	}
	return lines, rows.Err()
}

func (r *Repository) CreateOrderTender(ctx context.Context, t *dao.OrderTender) error {
	err := r.executor(ctx).QueryRowContext(ctx, `
		INSERT INTO order_tenders (order_id, kind, wallet_id, amount, status) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		t.OrderID, t.Kind, t.WalletID, t.Amount, t.Status).Scan(&t.ID)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to create order tender")
	}
	return nil
}

func (r *Repository) UpdateOrderTender(ctx context.Context, t *dao.OrderTender) error {
	if _, err := r.executor(ctx).ExecContext(ctx, `UPDATE order_tenders SET status = $2 WHERE id = $1`, t.ID, t.Status); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to update order tender")
	}
	return nil
}

func (r *Repository) ListOrderTenders(ctx context.Context, orderID int64) ([]*dao.OrderTender, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT t.id, t.order_id, t.kind, t.wallet_id, t.amount, t.status, w.public_id
		FROM order_tenders t LEFT JOIN wallets w ON w.id = t.wallet_id
		WHERE t.order_id = $1 ORDER BY t.id`, orderID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list order tenders")
	}
	defer rows.Close()
	var tenders []*dao.OrderTender
	for rows.Next() {
		var t dao.OrderTender
		if err := rows.Scan(&t.ID, &t.OrderID, &t.Kind, &t.WalletID, &t.Amount, &t.Status, &t.WalletPublicID); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan order tender")
		}
		tenders = append(tenders, &t)
	}
	return tenders, rows.Err()
}

// GetOrderPayment returns the gateway payment collecting an order's card
// share, if it has one.
func (r *Repository) GetOrderPayment(ctx context.Context, orderID int64) (*dao.Payment, error) {
	return r.queryPayment(ctx, `SELECT `+paymentColumns+paymentFrom+` WHERE p.order_id = $1 ORDER BY p.id DESC LIMIT 1`, orderID)
}
