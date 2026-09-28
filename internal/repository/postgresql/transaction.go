package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync/atomic"

	"github.com/gofreego/goutils/customerrors"
	"github.com/gofreego/goutils/logger"
)

// txKey carries an in-flight transaction on the context. It is an empty struct
// type rather than a string so nothing outside this package can collide with it
// or reach the transaction.
type txKey struct{}

// Executor is the part of *sql.DB and *sql.Tx that repository methods use.
// Taking this instead of a concrete type is what lets a query run either
// standalone or inside a transaction without knowing which.
type Executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// executor returns the transaction bound to ctx, or the primary pool when there
// is none. Every query in this package goes through it.
//
// Reads deliberately use the primary too. Replicas are not configured (plan.md
// Q6), and routing reads to one later would silently break read-your-writes for
// anything mid-transaction — a bug worth opting into explicitly rather than
// inheriting from a helper.
func (r *Repository) executor(ctx context.Context) Executor {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return r.connManager.Primary()
}

// InTx reports whether ctx is already inside a transaction.
func InTx(ctx context.Context) bool {
	_, ok := ctx.Value(txKey{}).(*sql.Tx)
	return ok
}

// WithTx runs fn inside one transaction. See service.Repository for the contract.
func (r *Repository) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	// Already inside a transaction: join it. Opening a second connection here
	// would split one logical unit of work into two independent commits, and
	// could deadlock against the rows the outer transaction already holds.
	if InTx(ctx) {
		return fn(ctx)
	}

	tx, err := r.connManager.Primary().BeginTx(ctx, nil)
	if err != nil {
		return customerrors.New(customerrors.ERROR_CODE_DATABASE_CONNECTION_FAILED,
			"failed to begin transaction: %s", err.Error())
	}

	// A panic must not leave the transaction open holding locks. Roll back,
	// then let the panic continue unchanged.
	defer func() {
		if p := recover(); p != nil {
			rollback(ctx, tx)
			panic(p)
		}
	}()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		rollback(ctx, tx)
		return err
	}

	if err := tx.Commit(); err != nil {
		return customerrors.New(customerrors.ERROR_CODE_DATABASE_CONNECTION_FAILED,
			"failed to commit transaction: %s", err.Error())
	}
	return nil
}

var savepointSeq atomic.Int64

// WithSavepoint runs fn inside the current transaction such that a failure
// undoes only fn's writes, and the transaction carries on.
//
// Joining the outer transaction, as a nested WithTx does, is not enough when
// the caller means to recover from fn's error: an operation that fails after
// writing — a wallet credit refused by a limit checked under the lock, once
// its postings exist — would otherwise leave those rows behind in a
// transaction that goes on to commit.
func (r *Repository) WithSavepoint(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if !InTx(ctx) {
		return customerrors.New(customerrors.ERROR_CODE_DATABASE_CONNECTION_FAILED,
			"a savepoint needs an enclosing transaction")
	}
	name := "sp_" + strconv.FormatInt(savepointSeq.Add(1), 10)
	if _, err := r.executor(ctx).ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return customerrors.New(customerrors.ERROR_CODE_DATABASE_CONNECTION_FAILED,
			"failed to create savepoint: %s", err.Error())
	}

	defer func() {
		if p := recover(); p != nil {
			_, _ = r.executor(ctx).ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+name)
			panic(p)
		}
	}()

	if err := fn(ctx); err != nil {
		if _, rbErr := r.executor(ctx).ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+name); rbErr != nil {
			return customerrors.New(customerrors.ERROR_CODE_DATABASE_CONNECTION_FAILED,
				"failed to roll back to savepoint after %v: %s", err, rbErr.Error())
		}
		return err
	}
	_, err = r.executor(ctx).ExecContext(ctx, "RELEASE SAVEPOINT "+name)
	return err
}

// withReadSnapshot runs fn in a read-only REPEATABLE READ transaction, so every
// query in it sees the database as of one instant.
//
// A single statement is already consistent under READ COMMITTED; this is for
// reports built from several. Without it, each part of a ledger check or
// report would describe a slightly different ledger, and a finding in one
// could not be lined up against the figures in another.
func (r *Repository) withReadSnapshot(ctx context.Context, fn func(ctx context.Context) error) error {
	if InTx(ctx) {
		return fn(ctx)
	}

	tx, err := r.connManager.Primary().BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if err != nil {
		return customerrors.New(customerrors.ERROR_CODE_DATABASE_CONNECTION_FAILED,
			"failed to begin snapshot transaction: %s", err.Error())
	}
	// Read-only: there is nothing to commit, so always roll back.
	defer rollback(ctx, tx)

	return fn(context.WithValue(ctx, txKey{}, tx))
}

// rollback undoes the transaction, logging anything other than the benign case
// where it has already finished.
func rollback(ctx context.Context, tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		logger.Error(ctx, "failed to rollback transaction: %v", err)
	}
}
