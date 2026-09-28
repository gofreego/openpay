package filter

import (
	"github.com/gofreego/openpay/internal/constants"
	"github.com/gofreego/openpay/internal/models/dao"
)

// LedgerAccount narrows an account listing.
type LedgerAccount struct {
	Limit  int
	Offset int

	// ProductID keeps one product's accounts; PlatformOnly keeps those
	// belonging to no product. At most one is set.
	ProductID    *int64
	PlatformOnly bool

	// Type is optional; empty means any.
	Type dao.AccountType

	// CodePrefix matches the start of the account code, e.g. "psp:".
	CodePrefix string

	// Scope is required: the repository refuses a listing without one.
	Scope *ProductScope
}

func (f *LedgerAccount) WithDefaults() {
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
