package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	sqlutils "github.com/gofreego/goutils/databases/connections/sql"
	"github.com/gofreego/goutils/databases/connections/pgsql"
)

// These tests need a real PostgreSQL, because the behaviour under test *is*
// PostgreSQL's — row locking, SKIP LOCKED, unique constraints under contention.
//
//	make test-integration
//
// They run against openpay_test, never the development database, because they
// TRUNCATE the tables they exercise. Override with OPENPAY_TEST_PG_* if needed.
func testRepository(t *testing.T) *Repository {
	t.Helper()
	if os.Getenv("OPENPAY_TEST_POSTGRES") == "" {
		t.Skip("integration test: run `make test-integration` (needs docker compose up -d postgres)")
	}

	env := func(key, fallback string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fallback
	}

	cfg := &sqlutils.Config{
		Name: sqlutils.Postgres,
		Postgresql: sqlutils.PostgresqlConfig{
			Primary: pgsql.Config{
				Host:     env("OPENPAY_TEST_PG_HOST", "localhost"),
				Port:     5432,
				Username: env("OPENPAY_TEST_PG_USER", "openpay"),
				Password: env("OPENPAY_TEST_PG_PASSWORD", "openpay"),
				DBName:   env("OPENPAY_TEST_PG_DBNAME", "openpay_test"),
				SSLMode:  "disable",
			},
		},
	}

	repo, err := NewRepository(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect to test postgres: %v", err)
	}
	return repo
}

// setupTables creates two throwaway tables so the tests can prove that a
// rollback spans more than one of them — the whole reason WithTx exists.
func setupTables(t *testing.T, repo *Repository) {
	t.Helper()
	ctx := context.Background()
	db := repo.connManager.Primary()

	for _, table := range []string{"tx_test_a", "tx_test_b"} {
		if _, err := db.ExecContext(ctx,
			"CREATE TABLE IF NOT EXISTS "+table+" (id BIGSERIAL PRIMARY KEY, note TEXT NOT NULL)"); err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
		if _, err := db.ExecContext(ctx, "TRUNCATE "+table); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}

	t.Cleanup(func() {
		for _, table := range []string{"tx_test_a", "tx_test_b"} {
			if _, err := db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+table); err != nil {
				t.Logf("cleanup drop %s: %v", table, err)
			}
		}
	})
}

func insert(ctx context.Context, repo *Repository, table, note string) error {
	_, err := repo.executor(ctx).ExecContext(ctx, "INSERT INTO "+table+" (note) VALUES ($1)", note)
	return err
}

func count(t *testing.T, repo *Repository, table string) int {
	t.Helper()
	var n int
	if err := repo.connManager.Primary().
		QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestWithTxCommits(t *testing.T) {
	repo := testRepository(t)
	setupTables(t, repo)

	err := repo.WithTx(context.Background(), func(ctx context.Context) error {
		if err := insert(ctx, repo, "tx_test_a", "one"); err != nil {
			return err
		}
		return insert(ctx, repo, "tx_test_b", "two")
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	if got := count(t, repo, "tx_test_a"); got != 1 {
		t.Errorf("tx_test_a count = %d, want 1", got)
	}
	if got := count(t, repo, "tx_test_b"); got != 1 {
		t.Errorf("tx_test_b count = %d, want 1", got)
	}
}

// The test that justifies the whole abstraction: a failure after a successful
// write to a different table must undo that write too.
func TestWithTxRollsBackAcrossTables(t *testing.T) {
	repo := testRepository(t)
	setupTables(t, repo)

	boom := errors.New("boom")
	err := repo.WithTx(context.Background(), func(ctx context.Context) error {
		if err := insert(ctx, repo, "tx_test_a", "one"); err != nil {
			return err
		}
		if err := insert(ctx, repo, "tx_test_b", "two"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx error = %v, want %v", err, boom)
	}

	if got := count(t, repo, "tx_test_a"); got != 0 {
		t.Errorf("tx_test_a count = %d, want 0 — the first write was not rolled back", got)
	}
	if got := count(t, repo, "tx_test_b"); got != 0 {
		t.Errorf("tx_test_b count = %d, want 0 — the second write was not rolled back", got)
	}
}

func TestWithTxRollsBackOnPanic(t *testing.T) {
	repo := testRepository(t)
	setupTables(t, repo)

	func() {
		defer func() {
			if p := recover(); p == nil {
				t.Error("panic should propagate out of WithTx")
			}
		}()
		_ = repo.WithTx(context.Background(), func(ctx context.Context) error {
			if err := insert(ctx, repo, "tx_test_a", "one"); err != nil {
				return err
			}
			panic("something went very wrong")
		})
	}()

	if got := count(t, repo, "tx_test_a"); got != 0 {
		t.Errorf("tx_test_a count = %d, want 0 — a panic left the write committed", got)
	}
}

// Nested WithTx must join the outer transaction. If it opened a second one, the
// inner write would survive the outer rollback and atomicity would be a lie.
func TestNestedWithTxJoinsOuterTransaction(t *testing.T) {
	repo := testRepository(t)
	setupTables(t, repo)

	boom := errors.New("outer failed")
	err := repo.WithTx(context.Background(), func(ctx context.Context) error {
		if err := insert(ctx, repo, "tx_test_a", "outer"); err != nil {
			return err
		}

		if err := repo.WithTx(ctx, func(ctx context.Context) error {
			if !InTx(ctx) {
				t.Error("nested WithTx lost the transaction from context")
			}
			return insert(ctx, repo, "tx_test_b", "inner")
		}); err != nil {
			return err
		}

		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx error = %v, want %v", err, boom)
	}

	if got := count(t, repo, "tx_test_b"); got != 0 {
		t.Errorf("tx_test_b count = %d, want 0 — the nested write committed independently", got)
	}
}

func TestExecutorOutsideTransactionUsesPool(t *testing.T) {
	repo := testRepository(t)

	ctx := context.Background()
	if InTx(ctx) {
		t.Fatal("a fresh context should not be in a transaction")
	}
	if _, ok := repo.executor(ctx).(*sql.Tx); ok {
		t.Error("executor returned a transaction outside WithTx")
	}
}
