// Package media downloads attachments a client referenced by URL, for the one
// case that needs it: a remote image on its way to Gemini, which does not
// fetch URLs itself.
//
// This is the only place Polyglot makes an outbound request to an address a
// *client* chose. Everywhere else the destination comes from a provider an
// operator configured. That difference is the whole security posture of this
// package: private address ranges are refused unconditionally here, where a
// provider base URL only refuses them when BLOCK_PRIVATE_UPSTREAM says so.
// Without that, any caller holding an API key could use the gateway to probe
// the network it runs in, or read a cloud instance's metadata endpoint.
//
// It is off by default. Turning it on is an explicit decision to let clients
// name outbound destinations.
package media

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/qunqin24/polyglot/internal/canonical"
)

// DefaultMaxBytes caps one attachment. Fetched bytes become base64 in the
// upstream request, which is a third larger again, so this is deliberately
// well under the request body limit.
const DefaultMaxBytes = 20 << 20

const fetchTimeout = 20 * time.Second

// allowedTypes are the content types worth inlining. Restricting them keeps
// this from becoming a general-purpose fetch proxy that happens to live behind
// an LLM gateway.
var allowedTypes = []string{"image/", "application/pdf", "text/plain"}

type Fetcher struct {
	client   *http.Client
	maxBytes int64
	log      *slog.Logger
	// deny decides whether an address may be dialled. It is a field only so
	// the tests can point this package at a loopback server; production always
	// uses blocked, and nothing outside this package can change it.
	deny func(net.IP) bool
}

func NewFetcher(maxBytes int64, log *slog.Logger) *Fetcher {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	f := &Fetcher{maxBytes: maxBytes, log: log, deny: blocked}

	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
		// Control runs after DNS resolution with the concrete address about to
		// be dialled, which is what closes the DNS-rebinding hole: a name that
		// resolves to a public address on the first lookup and a private one
		// on the second is caught here, at connect time, every time —
		// including on a redirect.
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("media: cannot parse address %q", address)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("media: unresolvable address")
			}
			if f.deny(ip) {
				// The address is deliberately not echoed: the caller supplied
				// the URL and does not need this to be a scanner.
				return fmt.Errorf("media: refusing to fetch from a non-public address")
			}
			return nil
		},
	}

	f.client = &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			// No proxy: a proxy would do the connecting, and the address
			// check above would be inspecting the proxy instead of the
			// destination.
			Proxy:               nil,
			DialContext:         dialer.DialContext,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	return f
}

// blocked reports whether an address is one a client must not be able to reach
// through this gateway.
func blocked(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	// 100.64.0.0/10, the carrier-grade NAT range, is where a good deal of
	// container and cloud infrastructure lives and is not covered by
	// IsPrivate.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

// Inline replaces remote attachments with their bytes.
//
// It copies any message it changes rather than writing through the request's
// shared slices: the gateway hands the same canonical request to a second
// provider on a fallback, and that attempt must see the request as the client
// sent it, not as the previous attempt rewrote it.
//
// A fetch that fails is left alone. The encoding codec then reports the
// attachment as unsupported, which is the honest outcome — better than failing
// a whole conversation over one image.
func (f *Fetcher) Inline(ctx context.Context, req *canonical.Request, d *canonical.Diagnostics) {
	if f == nil || req == nil {
		return
	}
	// The gateway's per-attempt request is a shallow struct copy, so Messages
	// still shares its backing array with the original. Replacing a Content
	// slice in place would therefore be visible to the next attempt as well —
	// the array element is shared, not just the struct. Both levels are copied
	// on first write.
	copiedMessages := false
	for mi := range req.Messages {
		for pi, p := range req.Messages[mi].Content {
			if (p.Type != canonical.PartImage && p.Type != canonical.PartFile) || !p.Media.Remote() {
				continue
			}
			mime, data, err := f.fetch(ctx, p.Media.URL)
			if err != nil {
				d.Note("content", canonical.FidelityUnsupported,
					"%s could not be downloaded and was not forwarded: %v", p.Media.Describe(), err)
				continue
			}
			if !copiedMessages {
				req.Messages = append([]canonical.Message(nil), req.Messages...)
				copiedMessages = true
			}
			req.Messages[mi].Content = copyParts(req.Messages[mi].Content)
			fetched := *p.Media
			fetched.URL = ""
			fetched.Data = data
			if fetched.MIMEType == "" {
				fetched.MIMEType = mime
			}
			req.Messages[mi].Content[pi].Media = &fetched
			d.Note("content", canonical.FidelitySemantic,
				"%s was downloaded and sent inline: the target protocol does not fetch URLs", fetched.Describe())
		}
	}
}

// copyParts duplicates a content slice once, so the original request object is
// never written through.
func copyParts(in []canonical.ContentPart) []canonical.ContentPart {
	out := make([]canonical.ContentPart, len(in))
	copy(out, in)
	return out
}

func (f *Fetcher) fetch(ctx context.Context, raw string) (mime, data string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("invalid url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("only http and https are fetched")
	}
	if u.User != nil {
		return "", "", fmt.Errorf("a url with embedded credentials is not fetched")
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", fmt.Errorf("invalid url")
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("unreachable")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("returned %d", resp.StatusCode)
	}
	ct := strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0])
	if !allowedType(ct) {
		return "", "", fmt.Errorf("content type %q is not an attachment Polyglot inlines", ct)
	}

	// Read one byte past the cap so an oversized body is detected rather than
	// silently truncated into a corrupt image.
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("download failed")
	}
	if int64(len(body)) > f.maxBytes {
		return "", "", fmt.Errorf("larger than the %d MiB limit", f.maxBytes>>20)
	}
	if len(body) == 0 {
		return "", "", fmt.Errorf("empty response")
	}
	return ct, base64.StdEncoding.EncodeToString(body), nil
}

func allowedType(ct string) bool {
	for _, allowed := range allowedTypes {
		if strings.HasSuffix(allowed, "/") {
			if strings.HasPrefix(ct, allowed) {
				return true
			}
			continue
		}
		if ct == allowed {
			return true
		}
	}
	return false
}
