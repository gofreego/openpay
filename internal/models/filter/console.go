package filter

import (
	"github.com/gofreego/openpay/internal/constants"
	"github.com/gofreego/openpay/internal/models/dao"
)

// Order narrows an order listing.
type Order struct {
	Limit, Offset int
	ProductID     *int64
	CustomerID    *int64
	Status        dao.OrderStatus
	// Scope is required: the repository refuses a listing without one.
	Scope *ProductScope
}

func (f *Order) WithDefaults() { f.Limit, f.Offset = page(f.Limit, f.Offset) }

// Refund narrows a refund listing across payments.
type Refund struct {
	Limit, Offset int
	ProductID     *int64
	Status        dao.RefundStatus
	Scope         *ProductScope
}

func (f *Refund) WithDefaults() { f.Limit, f.Offset = page(f.Limit, f.Offset) }

// Audit narrows the audit log. Entries without a product are platform
// entries: only the whole-estate scope sees them.
type Audit struct {
	ResourceType, ResourceID, ActorID string
	Limit                             int
	BeforeID                          int64
	Scope                             *ProductScope
}

func (f *Audit) WithDefaults() { f.Limit, _ = page(f.Limit, 0) }

func page(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = constants.DefaultPageSize
	}
	if limit > constants.MaxPageSize {
		limit = constants.MaxPageSize
	}
	return limit, max(offset, 0)
}
