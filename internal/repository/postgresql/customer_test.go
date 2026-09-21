package postgresql

import (
	"context"
	"testing"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

func upsertCustomer(t *testing.T, repo *Repository, externalRef string) *dao.Customer {
	t.Helper()
	customer, _ := upsertCustomerCreated(t, repo, externalRef)
	return customer
}

func upsertCustomerCreated(t *testing.T, repo *Repository, externalRef string) (*dao.Customer, bool) {
	t.Helper()
	customer := &dao.Customer{PublicID: ids.New(ids.Customer), ExternalRef: externalRef}
	created, err := repo.UpsertCustomer(context.Background(), customer)
	if err != nil {
		t.Fatalf("upsert customer %q: %v", externalRef, err)
	}
	return customer, created
}

// merge turns loser into a tombstone pointing at survivor. Balance transfer is
// a ledger concern for a later phase; this is the identity half.
func merge(t *testing.T, repo *Repository, loser, survivor *dao.Customer) {
	t.Helper()
	if _, err := repo.connManager.Primary().ExecContext(context.Background(),
		`UPDATE customers SET status = 'merged', merged_into_customer_id = $1 WHERE id = $2`,
		survivor.ID, loser.ID); err != nil {
		t.Fatalf("merge customers: %v", err)
	}
}

// A customer is one person platform-wide, so the same OpenAuth id must always
// return the same record however many products ask for it.
func TestUpsertCustomerIsIdempotent(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)

	first, createdFirst := upsertCustomerCreated(t, repo, "openauth_user_1")
	second, createdSecond := upsertCustomerCreated(t, repo, "openauth_user_1")

	if first.ID != second.ID {
		t.Errorf("same external_ref produced two customers: %d and %d", first.ID, second.ID)
	}
	if first.PublicID != second.PublicID {
		t.Errorf("public id changed between upserts: %q then %q", first.PublicID, second.PublicID)
	}
	if second.Status != dao.CustomerActive {
		t.Errorf("status = %q, want active", second.Status)
	}
	if !createdFirst {
		t.Error("the first upsert should report created")
	}
	if createdSecond {
		t.Error("the second upsert should report the customer already existed")
	}

	// A repeat lookup must not count as a change: updated_at means "last time
	// they changed", not "last time anyone asked".
	if !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("updated_at moved on a repeat upsert: %v then %v", first.UpdatedAt, second.UpdatedAt)
	}
}

func TestUpsertCustomerDistinctPeople(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)

	a := upsertCustomer(t, repo, "openauth_user_1")
	b := upsertCustomer(t, repo, "openauth_user_2")

	if a.ID == b.ID {
		t.Error("two different OpenAuth ids collapsed into one customer")
	}
}

func TestGetCustomerByExternalRef(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)
	ctx := context.Background()

	created := upsertCustomer(t, repo, "openauth_user_1")

	loaded, err := repo.GetCustomerByExternalRef(ctx, "openauth_user_1")
	if err != nil {
		t.Fatalf("get by external ref: %v", err)
	}
	if loaded.ID != created.ID {
		t.Error("lookup returned a different customer")
	}

	if _, err := repo.GetCustomerByExternalRef(ctx, "nobody"); !apperrors.Is(err, apperrors.NotFound) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.NotFound)
	}
}

// After a merge, the old id must keep working. A product backend that stored it
// before the merge should not start getting 404s.
func TestLookupFollowsAMerge(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)
	ctx := context.Background()

	loser := upsertCustomer(t, repo, "openauth_dup")
	survivor := upsertCustomer(t, repo, "openauth_real")
	merge(t, repo, loser, survivor)

	t.Run("by external ref", func(t *testing.T) {
		got, err := repo.GetCustomerByExternalRef(ctx, "openauth_dup")
		if err != nil {
			t.Fatalf("the merged external ref should still resolve: %v", err)
		}
		if got.ID != survivor.ID {
			t.Errorf("resolved to customer %d, want the survivor %d", got.ID, survivor.ID)
		}
	})

	t.Run("by public id", func(t *testing.T) {
		got, err := repo.GetCustomerByPublicID(ctx, loser.PublicID)
		if err != nil {
			t.Fatalf("the merged public id should still resolve: %v", err)
		}
		if got.ID != survivor.ID {
			t.Errorf("resolved to customer %d, want the survivor %d", got.ID, survivor.ID)
		}
	})

	t.Run("upsert of a merged ref", func(t *testing.T) {
		got := upsertCustomer(t, repo, "openauth_dup")
		if got.ID != survivor.ID {
			t.Errorf("upsert resolved to %d, want the survivor %d", got.ID, survivor.ID)
		}
	})
}

// Merges can chain when a record is merged twice.
func TestLookupFollowsAMergeChain(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)

	first := upsertCustomer(t, repo, "openauth_a")
	second := upsertCustomer(t, repo, "openauth_b")
	final := upsertCustomer(t, repo, "openauth_c")

	merge(t, repo, first, second)
	merge(t, repo, second, final)

	got, err := repo.GetCustomerByExternalRef(context.Background(), "openauth_a")
	if err != nil {
		t.Fatalf("resolve chained merge: %v", err)
	}
	if got.ID != final.ID {
		t.Errorf("resolved to %d, want the end of the chain %d", got.ID, final.ID)
	}
}

// A cycle must fail loudly rather than hang every request for that customer.
func TestMergeCycleIsBounded(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)
	ctx := context.Background()

	a := upsertCustomer(t, repo, "openauth_a")
	b := upsertCustomer(t, repo, "openauth_b")
	merge(t, repo, a, b)
	merge(t, repo, b, a)

	done := make(chan error, 1)
	go func() {
		_, err := repo.GetCustomerByExternalRef(ctx, "openauth_a")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a merge cycle should be reported, not resolved")
		}
		if !apperrors.Is(err, apperrors.Internal) {
			t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.Internal)
		}
	case <-t.Context().Done():
		t.Fatal("resolving a merge cycle did not terminate")
	}
}

// The database refuses the states that would make merge resolution incoherent.
func TestCustomerMergeConstraints(t *testing.T) {
	repo := testRepository(t)
	truncateWallets(t, repo)
	ctx := context.Background()

	customer := upsertCustomer(t, repo, "openauth_user_1")
	db := repo.connManager.Primary()

	t.Run("merged without a target", func(t *testing.T) {
		_, err := db.ExecContext(ctx,
			`UPDATE customers SET status = 'merged' WHERE id = $1`, customer.ID)
		if err == nil {
			t.Error("a merged customer with no merge target was accepted")
		}
	})

	t.Run("target without merged status", func(t *testing.T) {
		other := upsertCustomer(t, repo, "openauth_user_2")
		_, err := db.ExecContext(ctx,
			`UPDATE customers SET merged_into_customer_id = $1 WHERE id = $2`, other.ID, customer.ID)
		if err == nil {
			t.Error("a merge target on an active customer was accepted")
		}
	})

	t.Run("self merge", func(t *testing.T) {
		_, err := db.ExecContext(ctx,
			`UPDATE customers SET status = 'merged', merged_into_customer_id = id WHERE id = $1`,
			customer.ID)
		if err == nil {
			t.Error("a customer merged into itself was accepted; lookups would loop")
		}
	})
}
