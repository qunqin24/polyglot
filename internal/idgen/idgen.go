// Package idgen produces the short random identifiers Polyglot hands out for
// responses, tool calls and API keys.
package idgen

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
)

// New returns 24 hex characters of randomness, enough to stand in for an
// upstream response id.
func New() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("idgen: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Secret returns a 32-byte URL-safe token for API keys and sessions.
func Secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("idgen: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
