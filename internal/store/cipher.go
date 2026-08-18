package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Cipher encrypts provider credentials at rest with AES-256-GCM. The key is a
// file next to the database so a stolen .db alone is not enough, and so
// "docker compose up" needs no key management from the user.
type Cipher struct {
	aead cipher.AEAD
}

// LoadCipher reads the key file, creating it on first run. POLYGLOT_SECRET_KEY
// overrides the file for deployments that inject secrets by environment.
func LoadCipher(path string) (*Cipher, error) {
	if env := strings.TrimSpace(os.Getenv("POLYGLOT_SECRET_KEY")); env != "" {
		return newCipher(deriveKey(env))
	}

	key, err := os.ReadFile(path)
	switch {
	case err == nil && len(key) == 32:
		// ok
	case err == nil:
		return nil, fmt.Errorf("secret key %s is %d bytes, want 32", path, len(key))
	case errors.Is(err, os.ErrNotExist):
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate secret key: %w", err)
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, fmt.Errorf("write secret key %s: %w", path, err)
		}
	default:
		return nil, fmt.Errorf("read secret key %s: %w", path, err)
	}
	return newCipher(key)
}

func newCipher(key []byte) (*Cipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// deriveKey stretches an arbitrary passphrase into 32 bytes.
func deriveKey(pass string) []byte {
	sum := sha256sum([]byte("polyglot-secret-v1:" + pass))
	return sum[:]
}

// Encrypt returns nonce||ciphertext. Empty input encrypts to nil so an unset
// credential stays unset.
func (c *Cipher) Encrypt(plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (c *Cipher) Decrypt(blob []byte) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	n := c.aead.NonceSize()
	if len(blob) < n {
		return "", errors.New("ciphertext too short")
	}
	plain, err := c.aead.Open(nil, blob[:n], blob[n:], nil)
	if err != nil {
		// Almost always a changed/lost secret.key.
		return "", errors.New("decrypt provider credential: key mismatch")
	}
	return string(plain), nil
}
