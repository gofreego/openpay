package filter

import (
	"github.com/gofreego/openpay/internal/constants"
	"github.com/gofreego/openpay/internal/models/dao"
)

// Payment narrows a payment listing.
type Payment struct {
	Limit  int
	Offset int

	ProductID  *int64
	CustomerID *int64
	Status     dao.PaymentStatus

	// Scope is required: the repository refuses a listing without one.
	Scope *ProductScope
}

func (f *Payment) WithDefaults() {
	if f.Limit <= 0 {
		f.Limit = constants.DefaultPageSize
	}
	if f.Limit > constants.MaxPageSize {
		f.Limit = constants.MaxPageSize
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
}
