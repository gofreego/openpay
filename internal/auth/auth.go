// Package auth turns request headers into an authenticated caller.
//
// OpenPay has two kinds of caller and authenticates them differently:
//
//   - Operators are people in the admin console. OpenAuth authenticates them
//     and opengate injects x-user-id and x-user-perms, so OpenPay has no login
//     of its own and never sees a password (plan.md U-D2).
//   - Services are product backends presenting a credential, which resolves to
//     exactly one product.
package auth

import (
	"context"
	"strings"

	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/secrets"

	"github.com/gofreego/goutils/logger"
)

// Repository is the storage the authenticator needs.
type Repository interface {
	GetCredentialByKeyID(ctx context.Context, keyID string) (*dao.ServiceCredential, *dao.Product, error)
	TouchCredentialUsed(ctx context.Context, id int64) error
}

type Authenticator struct {
	repo Repository
}

func New(repo Repository) *Authenticator {
	return &Authenticator{repo: repo}
}

// invalidCredentials is returned for every credential failure — unknown key,
// wrong secret, revoked credential, suspended product.
//
// They are deliberately indistinguishable. Telling a caller which one applies
// hands an attacker an oracle for enumerating valid key ids, and telling them
// "revoked" confirms the key was once real.
func invalidCredentials() error {
	return apperrors.New(apperrors.Unauthenticated, "invalid credentials")
}

// Authenticate resolves the caller from a header lookup function.
//
// It takes a getter rather than an http.Request or gRPC metadata because both
// edges must produce the same result, and the only difference between them is
// where the headers come from.
func (a *Authenticator) Authenticate(ctx context.Context, get func(key string) string) (appcontext.Caller, error) {
	caller := appcontext.CallerFromValues(get)

	authorization := strings.TrimSpace(get(appcontext.HeaderAuthorization))
	if authorization == "" {
		// No credential: either an operator (x-user-id present) or anonymous.
		// CallerFromValues has already decided which.
		return caller, nil
	}

	keyID, secret, err := parseBearer(authorization)
	if err != nil {
		return caller, err
	}

	credential, product, err := a.repo.GetCredentialByKeyID(ctx, keyID)
	if err != nil {
		// Unauthenticated already means "invalid credentials"; anything else is
		// a real failure and must not be reported as a bad credential.
		if apperrors.Is(err, apperrors.Unauthenticated) {
			return caller, invalidCredentials()
		}
		return caller, err
	}

	ok, err := secrets.Verify(secret, credential.SecretHash)
	if err != nil {
		return caller, err
	}
	if !ok {
		return caller, invalidCredentials()
	}
	if !credential.IsActive() {
		return caller, invalidCredentials()
	}
	// A live credential for a suspended product must not authenticate, or
	// suspending a product would not actually stop its traffic.
	if product.Status != dao.ProductActive {
		return caller, invalidCredentials()
	}

	// Best effort, and outside any transaction the caller may start: knowing a
	// key is still in use is useful before revoking it, but never worth failing
	// a payment over.
	if err := a.repo.TouchCredentialUsed(ctx, credential.ID); err != nil {
		logger.Warn(ctx, "failed to record credential use for %s: %v", credential.PublicID, err)
	}

	caller.Kind = appcontext.KindService
	caller.ProductID = product.ID
	caller.ProductCode = product.Code
	caller.CredentialID = credential.PublicID
	// A service credential is not a person, so it carries no operator identity
	// even if a stray x-user-id header came along with it.
	caller.UserID = ""
	caller.Permissions = nil

	return caller, nil
}

// parseBearer splits "Bearer <key_id>.<secret>".
func parseBearer(authorization string) (keyID, secret string, err error) {
	scheme, token, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", "", apperrors.New(apperrors.Unauthenticated,
			"authorization header must be in the form 'Bearer <key_id>.<secret>'")
	}

	keyID, secret, found = strings.Cut(strings.TrimSpace(token), ".")
	if !found || keyID == "" || secret == "" {
		return "", "", invalidCredentials()
	}
	return keyID, secret, nil
}
