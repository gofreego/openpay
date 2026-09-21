// Package secrets generates and verifies service credential secrets.
//
// # Why SHA-256 and not argon2id
//
// Slow key derivation (argon2id, bcrypt, scrypt) exists to make brute force
// expensive against *low-entropy human passwords*, where the search space is
// small enough that hash speed decides whether an attacker succeeds.
//
// These secrets are not passwords. They are 32 bytes straight from
// crypto/rand — 256 bits of entropy, never chosen by a person, never reused.
// Brute-forcing one is infeasible no matter how fast the hash is, so a slow KDF
// buys nothing here.
//
// It does cost something. argon2id at the OWASP-recommended settings takes
// roughly 25ms, and credentials are verified on *every* authenticated API call,
// not once at login. That is 25ms of latency and 19MB of memory churn per
// request, which for a payment being collected in real time is a poor trade for
// no added security. The usual workaround — caching verified credentials — then
// has to be invalidated on revocation, which is a correctness problem bought to
// solve a self-inflicted performance one.
//
// SHA-256 with a constant-time comparison is the right primitive for
// high-entropy bearer tokens. The stored digest is tagged with its algorithm so
// this decision can be revisited without a migration.
package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"

	"github.com/gofreego/openpay/pkg/apperrors"
)

const (
	// secretBytes is the entropy behind the whole argument above. Do not lower
	// it without revisiting the choice of hash.
	secretBytes = 32
	keyIDBytes  = 16

	algoSHA256 = "sha256"
)

// Generated is a freshly minted credential. Secret exists only here and in the
// response to whoever created it: it is never stored and cannot be shown again.
type Generated struct {
	KeyID      string
	Secret     string
	SecretHash string
}

// Generate mints a key id and secret, returning the secret in clear text once
// and the hash to persist.
func Generate(keyIDPrefix string) (*Generated, error) {
	secret, err := randomToken(secretBytes)
	if err != nil {
		return nil, err
	}
	keyID, err := randomToken(keyIDBytes)
	if err != nil {
		return nil, err
	}
	return &Generated{
		KeyID:      keyIDPrefix + "_" + keyID,
		Secret:     secret,
		SecretHash: Hash(secret),
	}, nil
}

// Hash returns the stored form of a secret, tagged with its algorithm.
//
// There is no salt. Salting defends against precomputation across many hashes
// of guessable inputs; against 256-bit random values that are never reused,
// there is nothing to precompute.
func Hash(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return "$" + algoSHA256 + "$" + base64.RawStdEncoding.EncodeToString(digest[:])
}

// Verify reports whether secret matches encodedHash.
//
// The comparison is constant time: comparing byte by byte leaks through timing
// how much of a guess was correct, which would let an attacker recover a secret
// one byte at a time instead of searching the whole space.
func Verify(secret, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 3 || parts[0] != "" {
		return false, apperrors.New(apperrors.Internal, "malformed credential hash")
	}

	switch parts[1] {
	case algoSHA256:
		expected, err := base64.RawStdEncoding.DecodeString(parts[2])
		if err != nil {
			return false, apperrors.Wrap(err, apperrors.Internal, "malformed credential hash digest")
		}
		actual := sha256.Sum256([]byte(secret))
		return subtle.ConstantTimeCompare(actual[:], expected) == 1, nil
	default:
		return false, apperrors.New(apperrors.Internal, "unsupported credential hash algorithm %q", parts[1])
	}
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", apperrors.Wrap(err, apperrors.Internal, "failed to generate random token")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
