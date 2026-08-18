// Package auth covers both of Polyglot's identities: the API keys clients use
// to call the gateway, and the single local admin session behind the WebUI.
package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/qunqin24/polyglot/internal/idgen"
	"github.com/qunqin24/polyglot/internal/store"
)

// KeyPrefix marks Polyglot's own keys so they are recognisable in logs and
// client configs.
const KeyPrefix = "pg_"

// SessionTTL is how long an admin stays signed in.
const SessionTTL = 30 * 24 * time.Hour

const (
	SessionCookie = "polyglot_session"
	CSRFCookie    = "polyglot_csrf"
	CSRFHeader    = "X-CSRF-Token"
)

type ctxKey int

const (
	ctxAPIKey ctxKey = iota
	ctxAdmin
)

// NewAPIKey mints a key. The plaintext is returned once and never stored.
func NewAPIKey() (plaintext, prefix, hash string) {
	plaintext = KeyPrefix + idgen.Secret()
	prefix = plaintext[:11]
	hash = store.HashToken(plaintext)
	return
}

func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// APIKeyFromContext returns the key that authenticated a gateway request.
func APIKeyFromContext(ctx context.Context) *store.APIKey {
	k, _ := ctx.Value(ctxAPIKey).(*store.APIKey)
	return k
}

func withAPIKey(ctx context.Context, k *store.APIKey) context.Context {
	return context.WithValue(ctx, ctxAPIKey, k)
}

// AdminFromContext returns the signed-in admin, if any.
func AdminFromContext(ctx context.Context) *store.Admin {
	a, _ := ctx.Value(ctxAdmin).(*store.Admin)
	return a
}

func withAdmin(ctx context.Context, a *store.Admin) context.Context {
	return context.WithValue(ctx, ctxAdmin, a)
}

// ExtractClientKey pulls the presented credential out of a request, covering
// all three protocols' conventions.
func ExtractClientKey(r *http.Request) string {
	if v := r.Header.Get("Authorization"); v != "" {
		if k, ok := strings.CutPrefix(v, "Bearer "); ok {
			return strings.TrimSpace(k)
		}
		if k, ok := strings.CutPrefix(v, "bearer "); ok {
			return strings.TrimSpace(k)
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("x-api-key"); v != "" { // Anthropic
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("x-goog-api-key"); v != "" { // Gemini
		return strings.TrimSpace(v)
	}
	if v := r.URL.Query().Get("key"); v != "" { // Gemini query form
		return strings.TrimSpace(v)
	}
	return ""
}

// NewSession creates a signed-in session and returns the cookies to set. The
// CSRF cookie is readable by JavaScript on purpose: the WebUI echoes it back
// in a header, which is the double-submit defence.
func NewSession(ctx context.Context, st *store.Store, adminID int64, userAgent string, secure bool) ([]*http.Cookie, error) {
	token := idgen.Secret()
	if err := st.CreateSession(ctx, store.HashToken(token), adminID, SessionTTL, userAgent); err != nil {
		return nil, err
	}
	csrf := idgen.Secret()
	return []*http.Cookie{
		{
			Name:     SessionCookie,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(SessionTTL.Seconds()),
		},
		{
			Name:     CSRFCookie,
			Value:    csrf,
			Path:     "/",
			HttpOnly: false,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(SessionTTL.Seconds()),
		},
	}, nil
}

// ClearSession deletes the server-side session and expires both cookies.
func ClearSession(ctx context.Context, st *store.Store, r *http.Request, secure bool) []*http.Cookie {
	if c, err := r.Cookie(SessionCookie); err == nil {
		st.DeleteSession(ctx, store.HashToken(c.Value))
	}
	expire := func(name string, httpOnly bool) *http.Cookie {
		return &http.Cookie{
			Name: name, Value: "", Path: "/", HttpOnly: httpOnly,
			Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
		}
	}
	return []*http.Cookie{expire(SessionCookie, true), expire(CSRFCookie, false)}
}
