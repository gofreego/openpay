package filter

import (
	"github.com/gofreego/openpay/internal/constants"
	"github.com/gofreego/openpay/internal/models/dao"
)

// Product narrows a product listing.
type Product struct {
	Limit  int
	Offset int

	// Search matches code or name, case-insensitively.
	Search string

	// Status is optional; empty means any.
	Status dao.ProductStatus
}

// WithDefaults bounds the page size, so a caller cannot ask for the whole table
// by omitting a limit or naming an enormous one.
func (f *Product) WithDefaults() {
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
