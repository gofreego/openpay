// Package testsupport gives integration tests outside the repository package
// a real database. It is imported only from _test files.
package testsupport

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	"github.com/gofreego/goutils/databases/connections/pgsql"
	sqlutils "github.com/gofreego/goutils/databases/connections/sql"
	_ "github.com/lib/pq"

	"github.com/gofreego/openpay/internal/repository/postgresql"
)

var (
	once    sync.Once
	repo    *postgresql.Repository
	repoErr error
)

// Repository returns a repository over the integration-test database,
// emptied, or skips the test when integration tests are not enabled.
//
// One shared pool: a pool per test exhausts PostgreSQL's connection limit.
func Repository(t *testing.T) *postgresql.Repository {
	t.Helper()
	if os.Getenv("OPENPAY_TEST_POSTGRES") == "" {
		t.Skip("integration test: run `make test-integration` (needs docker compose up -d postgres)")
	}
	once.Do(func() {
		repo, repoErr = postgresql.NewRepository(context.Background(), &sqlutils.Config{
			Name: sqlutils.Postgres,
			Postgresql: sqlutils.PostgresqlConfig{Primary: pgsql.Config{
				Host: "localhost", Port: 5432, Username: "openpay", Password: "openpay",
				DBName: "openpay_test", SSLMode: "disable",
			}},
		})
	})
	if repoErr != nil {
		t.Fatalf("connect to test postgres: %v", repoErr)
	}
	Truncate(t)
	return repo
}

// Truncate empties every table, over a connection of its own so the
// repository needs no test-only method. TRUNCATE bypasses the ledger's
// immutability triggers, which fire on DELETE only.
func Truncate(t *testing.T) {
	t.Helper()
	db, err := sql.Open("postgres", "host=localhost port=5432 user=openpay password=openpay dbname=openpay_test sslmode=disable")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`TRUNCATE provider_controls, withdrawals, beneficiaries, recon_breaks, settlement_items, settlements, order_refund_parts, order_refunds, order_tenders, order_line_items, disputes, refunds, provider_request_log, provider_events, payment_transitions, payment_attempts, payments, orders, items,
		wallets, ledger_check_runs, ledger_holds, ledger_postings, ledger_journals,
		ledger_balances, ledger_accounts, wallet_types, customers, audit_log, service_credentials,
		products, idempotency_keys, outbox_events RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate (have migrations run?): %v", err)
	}
}

// Exec runs a statement directly, for tests that corrupt or inspect state
// the repository deliberately offers no way to touch.
func Exec(t *testing.T, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("postgres", "host=localhost port=5432 user=openpay password=openpay dbname=openpay_test sslmode=disable")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// Query reads one row into dest, for tests that inspect state the
// repository has no reason to expose.
func Query(t *testing.T, query string, dest ...any) {
	t.Helper()
	db, err := sql.Open("postgres", "host=localhost port=5432 user=openpay password=openpay dbname=openpay_test sslmode=disable")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.QueryRow(query).Scan(dest...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}
