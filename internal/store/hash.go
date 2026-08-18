package store

import (
	"crypto/sha256"
	"encoding/hex"
)

func sha256sum(b []byte) [32]byte { return sha256.Sum256(b) }

// HashToken is the one-way function used for API keys and session tokens.
// Both are high-entropy random strings, so a fast hash is appropriate here;
// admin passwords use bcrypt instead.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
