package postgresql

import (
	"errors"

	"github.com/lib/pq"
)

// PostgreSQL SQLSTATE codes we act on.
// https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
)

// isUniqueViolation reports whether err is a duplicate key error.
//
// This is how uniqueness is detected rather than checking first and inserting
// second: only the constraint is atomic. A SELECT-then-INSERT leaves a window
// where two callers both find nothing and both insert.
func isUniqueViolation(err error) bool {
	return hasSQLState(err, codeUniqueViolation)
}

func isForeignKeyViolation(err error) bool {
	return hasSQLState(err, codeForeignKeyViolation)
}

func hasSQLState(err error, code string) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && string(pqErr.Code) == code
}
