package api

import (
	"net/http"
	"strings"

	"github.com/qunqin24/polyglot/internal/idgen"
	"github.com/qunqin24/polyglot/internal/telemetry"
)

// Every request that reaches Polyglot gets an id, and that id is the thread
// running through the structured log, the request log row and — when tracing
// is on — the trace. Without one, correlating a client's report of "a request
// failed at 14:02" with anything on this side is guesswork.

// RequestIDHeader is both what a caller may send and what Polyglot echoes.
const RequestIDHeader = "X-Request-Id"

// traceparentHeader is the W3C header that continues a caller's trace.
const traceparentHeader = "traceparent"

// maxClientRequestID bounds an id supplied by a caller. It ends up in a
// database column and in log lines, so it does not get to be arbitrary.
const maxClientRequestID = 64

// requestID assigns the id and echoes it. A caller-supplied X-Request-Id is
// reused when it is plausibly an id — printable, short, and without the
// characters that would let it forge a second field in a log line — because a
// caller that already correlates its own traffic gains nothing from us
// renaming it. Anything else is replaced silently rather than rejected: a
// malformed header is not a reason to fail an LLM request.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(r.Header.Get(RequestIDHeader))
		if id == "" {
			id = idgen.New()
		}
		w.Header().Set(RequestIDHeader, id)

		ctx := telemetry.WithRequestID(r.Context(), id)
		// A valid inbound traceparent makes this request a child of the
		// caller's span instead of the root of a new trace.
		if tp := r.Header.Get(traceparentHeader); tp != "" {
			if sc := telemetry.ParseTraceparent(tp); sc.TraceID != (telemetry.TraceID{}) {
				ctx = telemetry.WithParent(ctx, sc)
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func sanitizeRequestID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxClientRequestID {
		return ""
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':':
		default:
			return ""
		}
	}
	return s
}
