package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/store"
)

// Gateway returns middleware that authenticates a client API key and answers
// in the protocol the caller is speaking, so an OpenAI SDK sees an OpenAI
// error shape even when it fails auth.
func Gateway(st *store.Store, proto protocol.Name) func(http.Handler) http.Handler {
	touch := newTouchLimiter(st)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			presented := ExtractClientKey(r)
			if presented == "" {
				writeProtocolError(w, proto, canonical.Errorf(canonical.ErrAuthentication,
					"missing API key: send it as 'Authorization: Bearer <key>'"))
				return
			}
			key, err := st.APIKeyByHash(r.Context(), store.HashToken(presented))
			if err != nil || !key.Enabled {
				writeProtocolError(w, proto, canonical.Errorf(canonical.ErrAuthentication, "invalid API key"))
				return
			}
			if key.ExpiresAt != nil && !time.Now().Before(*key.ExpiresAt) {
				writeProtocolError(w, proto, &canonical.Error{Type: canonical.ErrAuthentication,
					Code: "api_key_expired", Message: "API key has expired"})
				return
			}
			touch.mark(key.ID)
			next.ServeHTTP(w, r.WithContext(withAPIKey(r.Context(), key)))
		})
	}
}

// Admin guards the WebUI's own API with the session cookie plus a
// double-submit CSRF token on state-changing requests.
func Admin(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(SessionCookie)
			if err != nil || c.Value == "" {
				writeJSONError(w, http.StatusUnauthorized, "not signed in")
				return
			}
			admin, err := st.SessionAdmin(r.Context(), store.HashToken(c.Value))
			if err != nil {
				writeJSONError(w, http.StatusUnauthorized, "session expired")
				return
			}
			if isStateChanging(r.Method) && !csrfOK(r) {
				writeJSONError(w, http.StatusForbidden, "missing or invalid CSRF token")
				return
			}
			next.ServeHTTP(w, r.WithContext(withAdmin(r.Context(), admin)))
		})
	}
}

func isStateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func csrfOK(r *http.Request) bool {
	c, err := r.Cookie(CSRFCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return subtleEqual(c.Value, r.Header.Get(CSRFHeader))
}

func subtleEqual(a, b string) bool {
	if len(a) != len(b) || a == "" {
		return false
	}
	var diff byte
	for i := range len(a) {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func writeProtocolError(w http.ResponseWriter, proto protocol.Name, e *canonical.Error) {
	codec, err := protocol.Get(proto)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status())
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": e.Message})
		return
	}
	w.Write(codec.EncodeError(e))
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// touchLimiter keeps last_used_at roughly current without writing to SQLite on
// every single request.
type touchLimiter struct {
	st *store.Store
	mu sync.Mutex
	at map[int64]time.Time
}

func newTouchLimiter(st *store.Store) *touchLimiter {
	return &touchLimiter{st: st, at: map[int64]time.Time{}}
}

func (t *touchLimiter) mark(id int64) {
	t.mu.Lock()
	last, ok := t.at[id]
	now := time.Now()
	if ok && now.Sub(last) < time.Minute {
		t.mu.Unlock()
		return
	}
	t.at[id] = now
	t.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		t.st.TouchAPIKey(ctx, id)
	}()
}
