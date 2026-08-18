package telemetry

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/qunqin24/polyglot/internal/canonical"
)

// Label discipline. Two rules decide everything in this file:
//
//  1. A label value must come from a bounded set. Provider and model names are
//     bounded because an operator configured them; a model name a client
//     invented is not, so it never becomes a label.
//  2. Nothing that identifies a request, a caller or a payload is ever a
//     label. Request ids, trace ids, API keys, prompts, URLs and error text
//     belong in logs and traces, where they cost one row rather than one
//     time series each.

const (
	// labelNone marks a dimension that never got a value — a request that
	// failed before routing has no provider and no upstream model.
	labelNone = "none"
	// labelUnrouted stands in for the model name a client asked for when it
	// resolved to nothing. Using the raw string here would let anyone mint
	// unlimited time series with a loop of typos.
	labelUnrouted = "unrouted"
	// maxLabelLen truncates a pathologically long configured name.
	maxLabelLen = 96
)

// sanitizeLabel bounds the length of an operator-configured value and strips
// control characters. Quoting is handled by the exposition writer.
func sanitizeLabel(s string) string {
	if s == "" {
		return labelNone
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return labelNone
	}
	if len(s) > maxLabelLen {
		s = s[:maxLabelLen]
		// Do not cut a multi-byte rune in half.
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

func boolLabel(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// Error classes. A bounded vocabulary, so an error label can never grow with
// the number of distinct upstream error messages. The message itself stays in
// the request log, where it has already been stripped of credentials.
const (
	classNone           = "none"
	classTimeout        = "timeout"
	classRateLimit      = "rate_limit"
	classAuthentication = "authentication"
	classPermission     = "permission"
	classInvalidRequest = "invalid_request"
	classNotFound       = "not_found"
	classOverloaded     = "overloaded"
	classUpstream5xx    = "upstream_5xx"
	classUpstream       = "upstream_error"
	classNetwork        = "network_error"
	classCancelled      = "cancelled"
	classUnsupported    = "unsupported"
	classInternal       = "internal"
	classUnknown        = "unknown"
)

// ErrorClass maps a canonical error onto the bounded vocabulary above. It
// reads only the type and the status code — never the message, which can carry
// whatever an upstream chose to echo back, including the prompt.
func ErrorClass(e *canonical.Error) string {
	if e == nil {
		return classNone
	}
	switch e.Type {
	case canonical.ErrTimeout:
		return classTimeout
	case canonical.ErrRateLimit:
		return classRateLimit
	case canonical.ErrAuthentication:
		return classAuthentication
	case canonical.ErrPermission:
		return classPermission
	case canonical.ErrInvalidRequest:
		return classInvalidRequest
	case canonical.ErrNotFound:
		return classNotFound
	case canonical.ErrOverloaded:
		return classOverloaded
	case canonical.ErrUnsupported:
		return classUnsupported
	case canonical.ErrInternal:
		return classInternal
	case canonical.ErrUpstream:
		// The gateway uses ErrUpstream both for "the upstream answered 5xx"
		// and for "we could not reach it at all"; the status code separates
		// them, and they call for different fixes.
		switch {
		case e.StatusCode >= 500:
			return classUpstream5xx
		case e.StatusCode == 0:
			return classNetwork
		default:
			return classUpstream
		}
	}
	if e.StatusCode >= 500 {
		return classUpstream5xx
	}
	return classUnknown
}

// statusCodeLabel keeps the HTTP status bounded. Real codes are already a
// small set, but a nonsense value from an upstream must not become a series.
func statusCodeLabel(code int) string {
	switch {
	case code == 0:
		return labelNone
	case code < 100 || code > 599:
		return "other"
	default:
		return strconv.Itoa(code)
	}
}
