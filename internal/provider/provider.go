// Package provider knows how to *call* an upstream service: URLs, auth
// headers, transport. It knows nothing about message formats — that is the
// protocol codec's job. Keeping the two apart is what lets OpenRouter,
// DeepSeek and SiliconFlow share one codec while differing here.
package provider

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/qunqin24/polyglot/internal/protocol"
)

// Target is a resolved upstream: everything needed to make one call.
type Target struct {
	ID       int64
	Name     string
	Protocol protocol.Name
	BaseURL  string
	APIKey   string
	Headers  map[string]string
	Timeout  time.Duration
	// AutoDisableOnAuthError lets a rejected credential take this provider out
	// of rotation, rather than leaving it to fail every request until someone
	// notices. Opt-in, because 401 and 403 are not always about the key.
	AutoDisableOnAuthError bool
	// StrictFields stops request fields Polyglot does not recognise from being
	// replayed to this upstream. Set for an upstream that rejects members it
	// does not know; unset — the default — lets a provider's own parameters
	// through on a same-protocol route.
	StrictFields bool
}

// ValidateBaseURL rejects URLs that are malformed or that would let a
// misconfigured provider reach somewhere unexpected. It is called both when
// saving a provider and before every upstream call.
func ValidateBaseURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("base URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("base URL has no host")
	}
	if u.User != nil {
		return fmt.Errorf("base URL must not contain credentials")
	}
	return nil
}

// joinURL appends a path to a base URL, collapsing duplicate slashes and
// preserving any query string already present on the base.
func joinURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	path = strings.TrimLeft(path, "/")
	if path == "" {
		return base
	}
	return base + "/" + path
}

// ensureSuffix appends version to the base path when the user did not already
// include it, so both "https://api.anthropic.com" and
// "https://api.anthropic.com/v1" work.
func ensureSuffix(base, version string) string {
	trimmed := strings.TrimRight(base, "/")
	if strings.HasSuffix(trimmed, "/"+version) {
		return trimmed
	}
	return trimmed + "/" + version
}
