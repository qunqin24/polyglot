package gateway

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/qunqin24/polyglot/internal/canonical"
)

// Working out what a request was for.
//
// The other log columns record what a request used — a model, a key, a
// provider. None of them answers "which of my projects was this", which is the
// question you have when a bill looks wrong or a key looks busy.
//
// Three sources answer it, and they are tried in order of how deliberate they
// are. A caller that went to the trouble of naming itself is believed over one
// that merely has a User-Agent.
//
// Only these named headers are read. Polyglot never snapshots the header set:
// a snapshot filtered afterwards is one forgotten case away from writing an
// Authorization value into the database, while reading three headers by name
// cannot reach one.
const (
	headerAppTitle = "X-Title"      // OpenRouter's attribution convention
	headerReferer  = "HTTP-Referer" // its companion, and the misspelling the web made standard
	headerReferer2 = "Referer"
)

// maxAppLen bounds a client-supplied name before it reaches the database.
const maxAppLen = 200

// clientApp names what made the call.
//
//	X-Title            an app that named itself
//	HTTP-Referer host  an app that identified its site
//	User-Agent product the software, which costs the caller nothing
//
// The last one is what makes this useful without changing a single client:
// Claude Code, Cursor, curl and a Python script are already distinguishable
// today, and every request so far was throwing that away.
func clientApp(r *http.Request) string {
	if title := strings.TrimSpace(r.Header.Get(headerAppTitle)); title != "" {
		return sanitizeApp(title)
	}
	for _, h := range []string{headerReferer, headerReferer2} {
		if ref := strings.TrimSpace(r.Header.Get(h)); ref != "" {
			if u, err := url.Parse(ref); err == nil && u.Host != "" {
				return sanitizeApp(u.Host)
			}
			return sanitizeApp(ref)
		}
	}
	return sanitizeApp(userAgentProduct(r.Header.Get("User-Agent")))
}

// userAgentProduct keeps the leading product token and drops the parenthesised
// platform detail, so "OpenAI/Python 1.2.3 (Linux; x86_64)" becomes
// "OpenAI/Python 1.2.3" — enough to tell two callers apart, without recording
// someone's operating system build.
func userAgentProduct(ua string) string {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return ""
	}
	if i := strings.Index(ua, "("); i > 0 {
		ua = strings.TrimSpace(ua[:i])
	}
	if fields := strings.Fields(ua); len(fields) > 2 {
		ua = strings.Join(fields[:2], " ")
	}
	return ua
}

// sanitizeApp strips control characters and bounds the length. The value is
// client-supplied and ends up in a database column and a web page.
func sanitizeApp(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > maxAppLen {
		s = strings.ToValidUTF8(s[:maxAppLen], "")
	}
	return s
}

// requestLabels renders the client's own metadata for the log.
//
// This is the precise answer when you control the caller: a request tagged
// {"project": "docs-site"} says exactly what it was for. Keys are sorted so
// the stored string is stable and two identical requests compare equal.
func requestLabels(req *canonical.Request) string {
	if req == nil || len(req.Metadata) == 0 {
		return ""
	}
	keys := make([]string, 0, len(req.Metadata))
	for k := range req.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	ordered := make(map[string]string, len(keys))
	for _, k := range keys {
		ordered[k] = req.Metadata[k]
	}
	b, err := json.Marshal(ordered)
	if err != nil {
		return ""
	}
	return string(b)
}
