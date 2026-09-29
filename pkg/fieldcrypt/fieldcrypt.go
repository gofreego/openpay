// Package fieldcrypt encrypts individual sensitive fields — bank account
// numbers — before they reach the database, so a database dump or a backup
// does not hand over customers' accounts.
//
// Values are AES-256-GCM sealed and tagged with the id of the key that sealed
// them ("v1:<key id>:<base64>"), so keys can be rotated: new values use the
// current key, old ones still open with theirs, and nothing has to be
// re-encrypted at once. Fingerprints are keyed from the current key too, so
// after a rotation every value must be re-sealed (withdrawal.ResealAccounts,
// run by the worker at startup) for duplicate detection to keep working.
package fieldcrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Config names the keys. Keys maps a key id to a base64 32-byte key; the
// current one seals new values. Keep real keys in a secret store, never in
// a committed file.
type Config struct {
	CurrentKeyID string            `yaml:"CurrentKeyID"`
	Keys         map[string]string `yaml:"Keys"`
}

type Cipher struct {
	current string
	aeads   map[string]cipher.AEAD
	// fingerprint keys the HMAC that lets equal plaintexts be found without
	// decrypting: the current key's material, so it is as secret as the data.
	fingerprint []byte
}

// New builds a cipher. It refuses to run without a current key rather than
// fall back to storing plaintext.
func New(cfg Config) (*Cipher, error) {
	if cfg.CurrentKeyID == "" || cfg.Keys[cfg.CurrentKeyID] == "" {
		return nil, fmt.Errorf("fieldcrypt: no current key configured")
	}
	c := &Cipher{current: cfg.CurrentKeyID, aeads: map[string]cipher.AEAD{}}
	for id, encoded := range cfg.Keys {
		if strings.Contains(id, ":") {
			return nil, fmt.Errorf("fieldcrypt: key id %q may not contain ':'", id)
		}
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("fieldcrypt: key %q must be 32 bytes, base64-encoded", id)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		c.aeads[id] = aead
		if id == cfg.CurrentKeyID {
			mac := hmac.New(sha256.New, key)
			mac.Write([]byte("fieldcrypt fingerprint"))
			c.fingerprint = mac.Sum(nil)
		}
	}
	return c, nil
}

// CurrentKeyID is the key new values are sealed under.
func (c *Cipher) CurrentKeyID() string { return c.current }

// IsSealed reports whether value is a sealed value (under any key), as
// opposed to plaintext.
func IsSealed(value string) bool { return strings.HasPrefix(value, "v1:") }

// Seal encrypts plaintext under the current key.
func (c *Cipher) Seal(plaintext string) (string, error) {
	aead := c.aeads[c.current]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), []byte(c.current))
	return "v1:" + c.current + ":" + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value sealed under any configured key.
func (c *Cipher) Open(value string) (string, error) {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return "", fmt.Errorf("fieldcrypt: not a sealed value")
	}
	aead, ok := c.aeads[parts[1]]
	if !ok {
		return "", fmt.Errorf("fieldcrypt: key %q is not configured", parts[1])
	}
	sealed, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || len(sealed) < aead.NonceSize() {
		return "", fmt.Errorf("fieldcrypt: malformed sealed value")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(parts[1]))
	if err != nil {
		return "", fmt.Errorf("fieldcrypt: value does not open: %w", err)
	}
	return string(plain), nil
}

// Fingerprint is a keyed, deterministic digest of a value: equal values give
// equal fingerprints, so uniqueness can be enforced on data that is stored
// encrypted, while the fingerprint alone reveals nothing without the key.
func (c *Cipher) Fingerprint(value string) string {
	mac := hmac.New(sha256.New, c.fingerprint)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
