package fieldcrypt_test

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/gofreego/openpay/pkg/fieldcrypt"
)

func key() string {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return base64.StdEncoding.EncodeToString(k)
}

func TestSealOpenAndRotate(t *testing.T) {
	k1, k2 := key(), key()
	old, err := fieldcrypt.New(fieldcrypt.Config{CurrentKeyID: "k1", Keys: map[string]string{"k1": k1}})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := old.Seal("123456789012")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "123456789012") {
		t.Fatal("sealed value contains the plaintext")
	}
	again, _ := old.Seal("123456789012")
	if again == sealed {
		t.Error("sealing the same value twice gave the same ciphertext")
	}

	// After rotation, new values use k2 and old ones still open.
	rotated, _ := fieldcrypt.New(fieldcrypt.Config{CurrentKeyID: "k2", Keys: map[string]string{"k1": k1, "k2": k2}})
	if plain, err := rotated.Open(sealed); err != nil || plain != "123456789012" {
		t.Errorf("old value after rotation: %q, %v", plain, err)
	}
	fresh, _ := rotated.Seal("x")
	if !strings.HasPrefix(fresh, "v1:k2:") {
		t.Errorf("new value %q not sealed under the current key", fresh)
	}

	// A value cannot be opened by a cipher that lacks its key, or be edited.
	onlyK2, _ := fieldcrypt.New(fieldcrypt.Config{CurrentKeyID: "k2", Keys: map[string]string{"k2": k2}})
	if _, err := onlyK2.Open(sealed); err == nil {
		t.Error("opened a value without its key")
	}
	tampered := sealed[:len(sealed)-4] + "AAAA"
	if _, err := old.Open(tampered); err == nil {
		t.Error("a tampered value opened")
	}
}

func TestFingerprint(t *testing.T) {
	c, _ := fieldcrypt.New(fieldcrypt.Config{CurrentKeyID: "k", Keys: map[string]string{"k": key()}})
	other, _ := fieldcrypt.New(fieldcrypt.Config{CurrentKeyID: "k", Keys: map[string]string{"k": key()}})
	if c.Fingerprint("a") != c.Fingerprint("a") || c.Fingerprint("a") == c.Fingerprint("b") {
		t.Error("fingerprints must be equal for equal values and differ otherwise")
	}
	if c.Fingerprint("a") == other.Fingerprint("a") {
		t.Error("fingerprints do not depend on the key")
	}
}

func TestRefusesToRunWithoutAKey(t *testing.T) {
	if _, err := fieldcrypt.New(fieldcrypt.Config{}); err == nil {
		t.Error("built a cipher with no key — it would have to store plaintext")
	}
}
