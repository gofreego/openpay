package auth

import (
	"context"
	"testing"

	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/secrets"
)

// fakeRepo stands in for storage. Credential persistence itself is covered by
// the postgresql integration tests; these are about the decisions auth makes.
type fakeRepo struct {
	credential *dao.ServiceCredential
	product    *dao.Product
	touched    bool
	touchErr   error
}

func (f *fakeRepo) GetCredentialByKeyID(_ context.Context, keyID string) (*dao.ServiceCredential, *dao.Product, error) {
	if f.credential == nil || f.credential.KeyID != keyID {
		return nil, nil, apperrors.New(apperrors.Unauthenticated, "invalid credentials")
	}
	return f.credential, f.product, nil
}

func (f *fakeRepo) TouchCredentialUsed(context.Context, int64) error {
	f.touched = true
	return f.touchErr
}

func fixture(t *testing.T) (*fakeRepo, string, string) {
	t.Helper()
	gen, err := secrets.Generate("opk")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	repo := &fakeRepo{
		credential: &dao.ServiceCredential{
			ID: 1, PublicID: "scr_1", ProductID: 7, KeyID: gen.KeyID,
			SecretHash: gen.SecretHash, Status: dao.CredentialActive,
		},
		product: &dao.Product{ID: 7, Code: "zshala", Status: dao.ProductActive},
	}
	return repo, gen.KeyID, gen.Secret
}

func headers(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func TestAuthenticateServiceCredential(t *testing.T) {
	repo, keyID, secret := fixture(t)

	caller, err := New(repo).Authenticate(context.Background(), headers(map[string]string{
		appcontext.HeaderAuthorization: "Bearer " + keyID + "." + secret,
	}))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if caller.Kind != appcontext.KindService {
		t.Errorf("kind = %q, want service", caller.Kind)
	}
	if caller.ProductID != 7 || caller.ProductCode != "zshala" {
		t.Errorf("product = %d/%q, want 7/zshala", caller.ProductID, caller.ProductCode)
	}
	if caller.CredentialID != "scr_1" {
		t.Errorf("credential id = %q, want scr_1 (needed for the audit trail)", caller.CredentialID)
	}
	if !repo.touched {
		t.Error("credential use was not recorded")
	}
}

// Every credential failure must look the same. Distinguishing them gives an
// attacker an oracle: "unknown key" enumerates valid key ids, and "revoked"
// confirms a key was once real.
func TestAuthenticateFailuresAreIndistinguishable(t *testing.T) {
	_, keyID, secret := fixture(t)

	cases := []struct {
		name  string
		setup func() (*fakeRepo, string)
	}{
		{"unknown key", func() (*fakeRepo, string) {
			repo, _, _ := fixture(t)
			return repo, "Bearer opk_nosuchkey.somesecret"
		}},
		{"wrong secret", func() (*fakeRepo, string) {
			repo, _, _ := fixture(t)
			return repo, "Bearer " + keyID + ".wrong-secret"
		}},
		{"revoked credential", func() (*fakeRepo, string) {
			repo, _, _ := fixture(t)
			repo.credential.KeyID = keyID
			repo.credential.Status = dao.CredentialRevoked
			return repo, "Bearer " + keyID + "." + secret
		}},
		{"suspended product", func() (*fakeRepo, string) {
			repo, _, _ := fixture(t)
			repo.credential.KeyID = keyID
			repo.product.Status = dao.ProductSuspended
			return repo, "Bearer " + keyID + "." + secret
		}},
	}

	var messages []string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, authorization := tc.setup()
			// The fixtures above regenerate secrets, so align the stored hash
			// with the secret this case intends to present.
			if tc.name == "revoked credential" || tc.name == "suspended product" {
				repo.credential.SecretHash = secrets.Hash(secret)
			}

			_, err := New(repo).Authenticate(context.Background(), headers(map[string]string{
				appcontext.HeaderAuthorization: authorization,
			}))
			if err == nil {
				t.Fatal("expected authentication to fail")
			}
			if !apperrors.Is(err, apperrors.Unauthenticated) {
				t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.Unauthenticated)
			}
			messages = append(messages, apperrors.From(err).Message())
		})
	}

	for i, msg := range messages {
		if msg != messages[0] {
			t.Errorf("case %d message %q differs from %q; failures must be indistinguishable",
				i, msg, messages[0])
		}
	}
}

// A revoked credential must stop working, or revocation is decorative.
func TestRevokedCredentialCannotAuthenticate(t *testing.T) {
	repo, keyID, secret := fixture(t)
	repo.credential.Status = dao.CredentialRevoked

	_, err := New(repo).Authenticate(context.Background(), headers(map[string]string{
		appcontext.HeaderAuthorization: "Bearer " + keyID + "." + secret,
	}))
	if !apperrors.Is(err, apperrors.Unauthenticated) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.Unauthenticated)
	}
}

// Suspending a product must actually stop its traffic.
func TestSuspendedProductCannotAuthenticate(t *testing.T) {
	repo, keyID, secret := fixture(t)
	repo.product.Status = dao.ProductSuspended

	_, err := New(repo).Authenticate(context.Background(), headers(map[string]string{
		appcontext.HeaderAuthorization: "Bearer " + keyID + "." + secret,
	}))
	if !apperrors.Is(err, apperrors.Unauthenticated) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.Unauthenticated)
	}
}

// A credential is not a person. A stray x-user-id alongside it must not grant
// operator identity or permissions.
func TestServiceCredentialDoesNotInheritOperatorIdentity(t *testing.T) {
	repo, keyID, secret := fixture(t)

	caller, err := New(repo).Authenticate(context.Background(), headers(map[string]string{
		appcontext.HeaderAuthorization: "Bearer " + keyID + "." + secret,
		appcontext.HeaderUserID:        "ops_42",
		appcontext.HeaderUserPerms:     "openpay:products:write",
	}))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if caller.Kind != appcontext.KindService {
		t.Errorf("kind = %q, want service", caller.Kind)
	}
	if caller.UserID != "" {
		t.Errorf("UserID = %q, want empty — a credential is not a person", caller.UserID)
	}
	if len(caller.Permissions) != 0 {
		t.Errorf("permissions = %v, want none", caller.Permissions)
	}
}

func TestAuthenticateOperator(t *testing.T) {
	repo, _, _ := fixture(t)

	caller, err := New(repo).Authenticate(context.Background(), headers(map[string]string{
		appcontext.HeaderUserID:    "ops_42",
		appcontext.HeaderUserPerms: "openpay:products:write",
	}))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if caller.Kind != appcontext.KindOperator {
		t.Errorf("kind = %q, want operator", caller.Kind)
	}
	if !caller.HasPermission(PermProductsWrite) {
		t.Errorf("permissions = %v, want the write permission", caller.Permissions)
	}
}

func TestAuthenticateAnonymous(t *testing.T) {
	repo, _, _ := fixture(t)

	caller, err := New(repo).Authenticate(context.Background(), headers(nil))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if caller.Kind != appcontext.KindAnonymous {
		t.Errorf("kind = %q, want anonymous", caller.Kind)
	}
	// Anonymous is still traceable.
	if caller.RequestID == "" {
		t.Error("anonymous callers should still get a request id")
	}
}

func TestAuthenticateRejectsMalformedAuthorization(t *testing.T) {
	repo, _, _ := fixture(t)

	for _, header := range []string{
		"Basic abc",
		"Bearer",
		"Bearer nodot",
		"Bearer .secretonly",
		"Bearer keyonly.",
		"garbage",
	} {
		_, err := New(repo).Authenticate(context.Background(), headers(map[string]string{
			appcontext.HeaderAuthorization: header,
		}))
		if !apperrors.Is(err, apperrors.Unauthenticated) {
			t.Errorf("header %q: error code = %q, want %q", header, apperrors.CodeOf(err), apperrors.Unauthenticated)
		}
	}
}

// Recording last use is a convenience. It must never fail the request that
// produced it, least of all a payment.
func TestTouchFailureDoesNotFailAuthentication(t *testing.T) {
	repo, keyID, secret := fixture(t)
	repo.touchErr = apperrors.New(apperrors.Internal, "database hiccup")

	caller, err := New(repo).Authenticate(context.Background(), headers(map[string]string{
		appcontext.HeaderAuthorization: "Bearer " + keyID + "." + secret,
	}))
	if err != nil {
		t.Fatalf("a failed last-used update must not fail authentication: %v", err)
	}
	if caller.Kind != appcontext.KindService {
		t.Errorf("kind = %q, want service", caller.Kind)
	}
}
