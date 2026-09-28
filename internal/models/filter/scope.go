package filter

import "slices"

// ProductScope is the set of products a caller may see (plan.md D8, U-D6).
//
// It is a required part of every product-scoped listing: repositories refuse
// a query without one rather than treating "no scope" as "everything". An
// unscoped query is then a loud error during development instead of a quiet
// cross-product leak in production.
type ProductScope struct {
	all bool
	ids []int64
}

// AllProducts is central ops' scope, and the system's own (background jobs).
func AllProducts() *ProductScope { return &ProductScope{all: true} }

// OnlyProducts is a product operator's or product backend's scope. With no
// ids it sees nothing — the right answer for an operator holding no scope.
func OnlyProducts(ids ...int64) *ProductScope {
	return &ProductScope{ids: slices.Clone(ids)}
}

// All reports whether the scope spans the whole estate, platform accounts included.
func (s *ProductScope) All() bool { return s.all }

// IDs are the products in a limited scope. Meaningless when All.
func (s *ProductScope) IDs() []int64 { return s.ids }

// Allows reports whether a row belonging to productID is visible. A nil
// product means a platform row — PSP receivables, bank, tax — which only the
// whole-estate scope sees.
func (s *ProductScope) Allows(productID *int64) bool {
	if s.all {
		return true
	}
	return productID != nil && slices.Contains(s.ids, *productID)
}

// AllowsProduct is Allows for a product known to exist.
func (s *ProductScope) AllowsProduct(productID int64) bool {
	return s.Allows(&productID)
}
