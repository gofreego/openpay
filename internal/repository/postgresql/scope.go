package postgresql

import (
	"github.com/lib/pq"

	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// scopeCondition turns a product scope into a WHERE condition on column,
// appending its argument through arg (which returns the placeholder).
//
// A nil scope is refused rather than read as "everything" (plan.md D8): a
// listing that forgot to pass one fails in development instead of leaking
// another product's rows in production. An empty limited scope matches
// nothing, and platform rows (column IS NULL) match only the whole-estate one.
func scopeCondition(scope *filter.ProductScope, column string, arg func(any) string) (string, error) {
	if scope == nil {
		return "", apperrors.New(apperrors.Internal,
			"product-scoped query on %s issued without a scope", column)
	}
	if scope.All() {
		return "", nil
	}
	if len(scope.IDs()) == 0 {
		return "FALSE", nil
	}
	return column + " = ANY(" + arg(pq.Array(scope.IDs())) + ")", nil
}
