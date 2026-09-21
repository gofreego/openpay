package postgresql

import (
	"context"
	"testing"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
	"github.com/gofreego/openpay/pkg/secrets"
)

func truncateCatalog(t *testing.T, repo *Repository) {
	t.Helper()
	// Order matters: audit_log and service_credentials reference products.
	if _, err := repo.connManager.Primary().ExecContext(context.Background(),
		"TRUNCATE audit_log, service_credentials, products RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate catalog tables (have migrations run?): %v", err)
	}
}

func newProduct(code string) *dao.Product {
	return &dao.Product{
		PublicID:        ids.New(ids.Product),
		Code:            code,
		Name:            "Test " + code,
		Status:          dao.ProductActive,
		DefaultCurrency: "INR",
	}
}

func createProduct(t *testing.T, repo *Repository, code string) *dao.Product {
	t.Helper()
	p := newProduct(code)
	if err := repo.CreateProduct(context.Background(), p); err != nil {
		t.Fatalf("create product %q: %v", code, err)
	}
	return p
}

func TestCreateAndGetProduct(t *testing.T) {
	repo := testRepository(t)
	truncateCatalog(t, repo)
	ctx := context.Background()

	created := createProduct(t, repo, "zshala")
	if created.ID == 0 || created.CreatedAt.IsZero() {
		t.Fatalf("create did not populate generated fields: %+v", created)
	}

	byPublic, err := repo.GetProductByPublicID(ctx, created.PublicID)
	if err != nil {
		t.Fatalf("get by public id: %v", err)
	}
	if byPublic.Code != "zshala" {
		t.Errorf("code = %q, want zshala", byPublic.Code)
	}

	byCode, err := repo.GetProductByCode(ctx, "zshala")
	if err != nil {
		t.Fatalf("get by code: %v", err)
	}
	if byCode.ID != created.ID {
		t.Errorf("lookup by code returned a different row")
	}
}

// The code appears in ledger account codes, so two products must never share it.
func TestProductCodeIsUnique(t *testing.T) {
	repo := testRepository(t)
	truncateCatalog(t, repo)

	createProduct(t, repo, "zshala")

	err := repo.CreateProduct(context.Background(), newProduct("zshala"))
	if err == nil {
		t.Fatal("creating a second product with the same code should fail")
	}
	if !apperrors.Is(err, apperrors.AlreadyExists) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.AlreadyExists)
	}
}

func TestGetProductNotFound(t *testing.T) {
	repo := testRepository(t)
	truncateCatalog(t, repo)

	_, err := repo.GetProductByPublicID(context.Background(), "prd_nope")
	if !apperrors.Is(err, apperrors.NotFound) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.NotFound)
	}
}

func TestUpdateProduct(t *testing.T) {
	repo := testRepository(t)
	truncateCatalog(t, repo)
	ctx := context.Background()

	created := createProduct(t, repo, "zshala")

	update := &dao.Product{PublicID: created.PublicID, Name: "Zshala Renamed", Status: dao.ProductSuspended}
	if err := repo.UpdateProduct(ctx, update); err != nil {
		t.Fatalf("update: %v", err)
	}

	reloaded, err := repo.GetProductByPublicID(ctx, created.PublicID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Name != "Zshala Renamed" {
		t.Errorf("name = %q, want the updated value", reloaded.Name)
	}
	if reloaded.Status != dao.ProductSuspended {
		t.Errorf("status = %q, want suspended", reloaded.Status)
	}
	// Code and currency are not updatable: ledger account codes embed the code.
	if reloaded.Code != "zshala" {
		t.Errorf("code = %q, want it unchanged", reloaded.Code)
	}
	if reloaded.DefaultCurrency != "INR" {
		t.Errorf("currency = %q, want it unchanged", reloaded.DefaultCurrency)
	}
}

func TestUpdateMissingProduct(t *testing.T) {
	repo := testRepository(t)
	truncateCatalog(t, repo)

	err := repo.UpdateProduct(context.Background(),
		&dao.Product{PublicID: "prd_nope", Name: "x", Status: dao.ProductActive})
	if !apperrors.Is(err, apperrors.NotFound) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.NotFound)
	}
}

func TestListProducts(t *testing.T) {
	repo := testRepository(t)
	truncateCatalog(t, repo)
	ctx := context.Background()

	createProduct(t, repo, "zshala")
	createProduct(t, repo, "bappaapp")
	suspended := createProduct(t, repo, "helpdesk")
	if err := repo.UpdateProduct(ctx, &dao.Product{
		PublicID: suspended.PublicID, Name: suspended.Name, Status: dao.ProductSuspended,
	}); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	t.Run("all", func(t *testing.T) {
		products, total, err := repo.ListProducts(ctx, &filter.Product{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 3 || len(products) != 3 {
			t.Errorf("got %d of total %d, want 3 of 3", len(products), total)
		}
	})

	t.Run("by status", func(t *testing.T) {
		products, total, err := repo.ListProducts(ctx, &filter.Product{Status: dao.ProductActive})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 2 || len(products) != 2 {
			t.Errorf("got %d of total %d, want 2 of 2", len(products), total)
		}
	})

	t.Run("by search", func(t *testing.T) {
		products, total, err := repo.ListProducts(ctx, &filter.Product{Search: "ZSHA"})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 1 || len(products) != 1 {
			t.Fatalf("got %d of total %d, want 1 of 1", len(products), total)
		}
		if products[0].Code != "zshala" {
			t.Errorf("code = %q, want zshala — search should be case-insensitive", products[0].Code)
		}
	})

	// The total must describe the whole filtered set, not the page, or a UI
	// cannot render pagination correctly.
	t.Run("paging keeps total of the full set", func(t *testing.T) {
		products, total, err := repo.ListProducts(ctx, &filter.Product{Limit: 2, Offset: 0})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(products) != 2 {
			t.Errorf("page size = %d, want 2", len(products))
		}
		if total != 3 {
			t.Errorf("total = %d, want 3 (the whole set, not the page)", total)
		}

		second, total, err := repo.ListProducts(ctx, &filter.Product{Limit: 2, Offset: 2})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(second) != 1 || total != 3 {
			t.Errorf("second page: got %d of %d, want 1 of 3", len(second), total)
		}
	})

	// An unbounded listing would let one caller pull the whole table.
	t.Run("limit is capped", func(t *testing.T) {
		f := &filter.Product{Limit: 10_000}
		if _, _, err := repo.ListProducts(ctx, f); err != nil {
			t.Fatalf("list: %v", err)
		}
		if f.Limit > 100 {
			t.Errorf("limit = %d, want it capped at the maximum page size", f.Limit)
		}
	})
}

func TestCreateAndAuthenticateCredential(t *testing.T) {
	repo := testRepository(t)
	truncateCatalog(t, repo)
	ctx := context.Background()

	product := createProduct(t, repo, "zshala")
	gen, err := secrets.Generate("opk")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	credential := &dao.ServiceCredential{
		PublicID:   ids.New(ids.ServiceCredential),
		ProductID:  product.ID,
		Name:       "zshala backend",
		KeyID:      gen.KeyID,
		SecretHash: gen.SecretHash,
		Status:     dao.CredentialActive,
	}
	if err := repo.CreateCredential(ctx, credential); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	loaded, loadedProduct, err := repo.GetCredentialByKeyID(ctx, gen.KeyID)
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if loadedProduct.Code != "zshala" {
		t.Errorf("product code = %q, want zshala — the product must come back with the credential", loadedProduct.Code)
	}
	ok, err := secrets.Verify(gen.Secret, loaded.SecretHash)
	if err != nil || !ok {
		t.Errorf("stored hash does not verify the generated secret: ok=%v err=%v", ok, err)
	}
}

// An unknown key must look exactly like a wrong secret to the caller: saying
// "no such key" tells an attacker which key ids exist.
func TestGetCredentialByUnknownKeyIsUnauthenticated(t *testing.T) {
	repo := testRepository(t)
	truncateCatalog(t, repo)

	_, _, err := repo.GetCredentialByKeyID(context.Background(), "opk_unknown")
	if !apperrors.Is(err, apperrors.Unauthenticated) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.Unauthenticated)
	}
	if apperrors.From(err).Message() == "invalid credentials" {
		return
	}
	t.Errorf("message = %q, want a message that does not reveal whether the key exists",
		apperrors.From(err).Message())
}

func TestRevokeCredential(t *testing.T) {
	repo := testRepository(t)
	truncateCatalog(t, repo)
	ctx := context.Background()

	product := createProduct(t, repo, "zshala")
	gen, _ := secrets.Generate("opk")
	credential := &dao.ServiceCredential{
		PublicID: ids.New(ids.ServiceCredential), ProductID: product.ID, Name: "backend",
		KeyID: gen.KeyID, SecretHash: gen.SecretHash, Status: dao.CredentialActive,
	}
	if err := repo.CreateCredential(ctx, credential); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	if err := repo.RevokeCredential(ctx, credential.PublicID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	loaded, _, err := repo.GetCredentialByKeyID(ctx, gen.KeyID)
	if err != nil {
		t.Fatalf("load after revoke: %v", err)
	}
	if loaded.IsActive() {
		t.Error("credential is still active after revocation")
	}
	if loaded.RevokedAt == nil {
		t.Error("revoked_at was not stamped")
	}

	// Revoking twice is the caller's intent already satisfied, not an error.
	if err := repo.RevokeCredential(ctx, credential.PublicID); err != nil {
		t.Errorf("second revoke should succeed, got %v", err)
	}
}

func TestCredentialRequiresExistingProduct(t *testing.T) {
	repo := testRepository(t)
	truncateCatalog(t, repo)

	gen, _ := secrets.Generate("opk")
	err := repo.CreateCredential(context.Background(), &dao.ServiceCredential{
		PublicID: ids.New(ids.ServiceCredential), ProductID: 999999, Name: "orphan",
		KeyID: gen.KeyID, SecretHash: gen.SecretHash, Status: dao.CredentialActive,
	})
	if !apperrors.Is(err, apperrors.NotFound) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.NotFound)
	}
}

func TestRecordAudit(t *testing.T) {
	repo := testRepository(t)
	truncateCatalog(t, repo)
	ctx := context.Background()

	product := createProduct(t, repo, "zshala")

	entry := &dao.AuditEntry{
		ActorType:    dao.ActorOperator,
		ActorID:      "ops_42",
		Action:       "product.created",
		ResourceType: "product",
		ResourceID:   product.PublicID,
		ProductID:    &product.ID,
		RequestID:    "req_1",
		After:        []byte(`{"code":"zshala"}`),
	}
	if err := repo.RecordAudit(ctx, entry); err != nil {
		t.Fatalf("record audit: %v", err)
	}
	if entry.ID == 0 || entry.CreatedAt.IsZero() {
		t.Error("audit entry did not get its generated fields")
	}

	// Creation has no before state; the column must be NULL rather than an
	// empty string, which JSONB rejects.
	var beforeIsNull bool
	if err := repo.connManager.Primary().QueryRowContext(ctx,
		"SELECT before IS NULL FROM audit_log WHERE id = $1", entry.ID).Scan(&beforeIsNull); err != nil {
		t.Fatalf("inspect audit row: %v", err)
	}
	if !beforeIsNull {
		t.Error("before should be NULL for a creation")
	}
}

// An audit row must not survive a transaction that rolled back: it would claim
// a change that never happened.
func TestAuditRollsBackWithTheChange(t *testing.T) {
	repo := testRepository(t)
	truncateCatalog(t, repo)
	ctx := context.Background()

	failed := apperrors.New(apperrors.Internal, "work failed")
	err := repo.WithTx(ctx, func(ctx context.Context) error {
		product := newProduct("ghost")
		if err := repo.CreateProduct(ctx, product); err != nil {
			return err
		}
		if err := repo.RecordAudit(ctx, &dao.AuditEntry{
			ActorType: dao.ActorOperator, ActorID: "ops_42", Action: "product.created",
			ResourceType: "product", ResourceID: product.PublicID,
		}); err != nil {
			return err
		}
		return failed
	})
	if err == nil {
		t.Fatal("expected the transaction to fail")
	}

	var count int
	if err := repo.connManager.Primary().
		QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_log").Scan(&count); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if count != 0 {
		t.Errorf("audit rows = %d, want 0 — an audit entry outlived the change it describes", count)
	}
}
