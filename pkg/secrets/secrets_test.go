package secrets

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateProducesVerifiableCredential(t *testing.T) {
	gen, err := Generate("opk")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if !strings.HasPrefix(gen.KeyID, "opk_") {
		t.Errorf("KeyID = %q, want the given prefix", gen.KeyID)
	}
	if gen.Secret == "" {
		t.Fatal("Secret is empty")
	}
	// The clear-text secret must never appear in what gets persisted.
	if strings.Contains(gen.SecretHash, gen.Secret) {
		t.Fatal("the stored hash contains the secret in clear text")
	}

	ok, err := Verify(gen.Secret, gen.SecretHash)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("the generated secret does not verify against its own hash")
	}
}

// The entire choice of hash rests on these secrets being high entropy, so the
// entropy itself is worth asserting.
func TestGeneratedSecretCarriesFullEntropy(t *testing.T) {
	gen, err := Generate("opk")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	raw, err := base64.RawURLEncoding.DecodeString(gen.Secret)
	if err != nil {
		t.Fatalf("secret is not valid base64url: %v", err)
	}
	if len(raw) != secretBytes {
		t.Errorf("secret carries %d bytes, want %d — the hash choice assumes 256 bits", len(raw), secretBytes)
	}
}

func TestGenerateIsUnique(t *testing.T) {
	seen := map[string]struct{}{}
	for range 200 {
		gen, err := Generate("opk")
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if _, dup := seen[gen.Secret]; dup {
			t.Fatal("Generate produced a duplicate secret")
		}
		if _, dup := seen[gen.KeyID]; dup {
			t.Fatal("Generate produced a duplicate key id")
		}
		seen[gen.Secret] = struct{}{}
		seen[gen.KeyID] = struct{}{}
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	hash := Hash("correct-secret")

	for _, wrong := range []string{"", "correct-secre", "correct-secret ", "CORRECT-SECRET", "wrong"} {
		ok, err := Verify(wrong, hash)
		if err != nil {
			t.Fatalf("Verify(%q): %v", wrong, err)
		}
		if ok {
			t.Errorf("Verify(%q) accepted a wrong secret", wrong)
		}
	}
}

func TestHashIsDeterministic(t *testing.T) {
	// Unlike a salted password hash, this is intentionally deterministic:
	// lookup is by key_id and the secret is never guessable, so there is
	// nothing for a salt to defend against.
	if Hash("same-secret") != Hash("same-secret") {
		t.Error("hashing the same secret twice produced different output")
	}
	if Hash("a") == Hash("b") {
		t.Error("different secrets hashed to the same value")
	}
}

func TestHashIsTaggedWithItsAlgorithm(t *testing.T) {
	hash := Hash("secret")

	parts := strings.Split(hash, "$")
	if len(parts) != 3 {
		t.Fatalf("hash %q has %d segments, want 3", hash, len(parts))
	}
	if parts[1] != algoSHA256 {
		t.Errorf("algorithm tag = %q, want %q", parts[1], algoSHA256)
	}
	// The tag is what allows the algorithm to change later without a migration.
	if _, err := Verify("secret", "$notanalgo$"+parts[2]); err == nil {
		t.Error("Verify accepted an unknown algorithm tag")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	for _, bad := range []string{
		"",
		"not-a-hash",
		"$sha256$",
		"$sha256$!!!not-base64!!!",
		"sha256$abc",
		"$sha256$abc$extra",
	} {
		if ok, err := Verify("secret", bad); err == nil && ok {
			t.Errorf("Verify accepted malformed hash %q", bad)
		}
	}
}
