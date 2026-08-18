// Package setup owns the one-time credential that protects first-run setup.
package setup

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/qunqin24/polyglot/internal/idgen"
)

const (
	// HeaderName is deliberately a header rather than a query parameter so the
	// setup credential does not land in browser history or access logs.
	HeaderName = "X-Polyglot-Setup-Token"
	filename   = "setup.token"
	minLength  = 16
)

// Guard validates and consumes the credential for one fresh installation.
// The database remains the final authority on whether setup is still open;
// this guard only decides who is allowed to attempt it.
type Guard struct {
	mu    sync.RWMutex
	token string
	path  string
}

// LoadOrCreate uses the operator-supplied token when present. Otherwise it
// keeps a generated token in DATA_DIR so restarts do not strand an install.
func LoadOrCreate(dataDir, configured string) (*Guard, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		if err := validate(configured); err != nil {
			return nil, fmt.Errorf("POLYGLOT_SETUP_TOKEN: %w", err)
		}
		return &Guard{token: configured}, nil
	}

	path := filepath.Join(dataDir, filename)
	if body, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(body))
		if err := validate(token); err != nil {
			return nil, fmt.Errorf("read setup token %s: %w", path, err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure setup token %s: %w", path, err)
		}
		return &Guard{token: token, path: path}, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read setup token %s: %w", path, err)
	}

	token := idgen.Secret()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create setup token %s: %w", path, err)
	}
	if _, err := fmt.Fprintln(f, token); err != nil {
		f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write setup token %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close setup token %s: %w", path, err)
	}
	return &Guard{token: token, path: path}, nil
}

// New returns an in-memory guard. It is useful to callers that already own a
// credential lifecycle, including the API's isolated tests.
func New(token string) (*Guard, error) {
	token = strings.TrimSpace(token)
	if err := validate(token); err != nil {
		return nil, err
	}
	return &Guard{token: token}, nil
}

func validate(token string) error {
	if len(token) < minLength {
		return fmt.Errorf("must be at least %d characters", minLength)
	}
	return nil
}

// Valid compares fixed-size hashes so a wrong token's length does not change
// the comparison path.
func (g *Guard) Valid(candidate string) bool {
	if g == nil {
		return false
	}
	g.mu.RLock()
	token := g.token
	g.mu.RUnlock()
	if token == "" {
		return false
	}
	want := sha256.Sum256([]byte(token))
	got := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

// Consume invalidates the in-memory credential before removing its file. The
// administrator row already closes setup if file removal happens to fail.
func (g *Guard) Consume() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	g.token = ""
	path := g.path
	g.mu.Unlock()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove setup token %s: %w", path, err)
	}
	return nil
}

// Path names the generated credential file without exposing its contents.
func (g *Guard) Path() string {
	if g == nil {
		return ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.path
}

// RemoveStaleFile cleans up the narrow crash window after the administrator
// row commits but before Consume removes the one-time file.
func RemoveStaleFile(dataDir string) error {
	path := filepath.Join(dataDir, filename)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale setup token %s: %w", path, err)
	}
	return nil
}
