package media

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/internal/canonical"
)

func testFetcher(t *testing.T, maxBytes int64) *Fetcher {
	t.Helper()
	return NewFetcher(maxBytes, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// localFetcher is the same fetcher with loopback allowed, so the success path
// can be driven against an httptest server. Only the address predicate is
// swapped: the scheme check, content-type check, size cap and redirect limit
// are the production ones. The refusal tests use the real predicate.
func localFetcher(t *testing.T, maxBytes int64) *Fetcher {
	t.Helper()
	f := testFetcher(t, maxBytes)
	f.deny = func(ip net.IP) bool { return !ip.IsLoopback() && blocked(ip) }
	return f
}

func imageRequest(url string) *canonical.Request {
	return &canonical.Request{
		Messages: []canonical.Message{{
			Role: canonical.RoleUser,
			Content: []canonical.ContentPart{
				canonical.Text("look at this"),
				{Type: canonical.PartImage, Media: &canonical.Media{URL: url}},
			},
		}},
	}
}

func mediaOf(req *canonical.Request) *canonical.Media {
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Type == canonical.PartImage {
				return p.Media
			}
		}
	}
	return nil
}

// This is the whole reason the package exists: Gemini will not fetch a URL, so
// the bytes have to arrive inline.
func TestARemoteImageIsDownloadedAndInlined(t *testing.T) {
	const png = "\x89PNG\r\n\x1a\nfake"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, png)
	}))
	defer srv.Close()

	req := imageRequest(srv.URL + "/cat.png")
	d := canonical.NewDiagnostics()
	localFetcher(t, 0).Inline(context.Background(), req, d)

	m := mediaOf(req)
	if m.Data == "" {
		t.Fatalf("the image was not inlined: %+v", m)
	}
	if m.URL != "" {
		t.Errorf("the url should be cleared once the bytes are inline: %q", m.URL)
	}
	if m.MIMEType != "image/png" {
		t.Errorf("mime = %q, want the served content type", m.MIMEType)
	}
	raw, err := base64.StdEncoding.DecodeString(m.Data)
	if err != nil || string(raw) != png {
		t.Errorf("payload did not survive: %q (%v)", raw, err)
	}
	// The substitution is a change of representation, so it is recorded.
	if !d.Lossy() && len(d.All()) == 0 {
		t.Error("inlining an image should leave a note")
	}
}

// A client picks this URL. Left unchecked, anyone holding an API key could use
// the gateway to reach the network it runs in, or a cloud metadata endpoint.
func TestPrivateAndLocalAddressesAreRefused(t *testing.T) {
	// A real listener on loopback: the guard must refuse it even though it is
	// reachable and answers correctly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, "secret")
	}))
	defer srv.Close()

	targets := []string{
		srv.URL + "/x.png",              // 127.0.0.1
		"http://169.254.169.254/latest", // cloud metadata
		"http://10.0.0.5/x.png",
		"http://192.168.1.1/x.png",
		"http://[::1]/x.png",
		"http://100.100.100.200/x.png", // carrier-grade NAT
	}
	f := testFetcher(t, 0)
	for _, target := range targets {
		req := imageRequest(target)
		d := canonical.NewDiagnostics()
		f.Inline(context.Background(), req, d)

		if m := mediaOf(req); m.Data != "" {
			t.Errorf("%s was fetched; it must be refused", target)
		}
		if !d.Lossy() {
			t.Errorf("%s was refused with no note", target)
		}
	}
}

func TestOnlyHTTPSchemesAndAttachmentTypesAreFetched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html>not an image</html>")
	}))
	defer srv.Close()

	for _, target := range []string{
		"file:///etc/passwd",
		"gopher://example.com/x",
		"ftp://example.com/x.png",
		"http://user:pass@example.com/x.png",
		srv.URL + "/page.html", // reachable and loopback-allowed, but not an attachment
	} {
		req := imageRequest(target)
		localFetcher(t, 0).Inline(context.Background(), req, canonical.NewDiagnostics())
		if m := mediaOf(req); m.Data != "" {
			t.Errorf("%s should not have been inlined", target)
		}
	}
}

func TestAnOversizedDownloadIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(make([]byte, 4096))
	}))
	defer srv.Close()

	req := imageRequest(srv.URL + "/big.png")
	d := canonical.NewDiagnostics()
	localFetcher(t, 1024).Inline(context.Background(), req, d)

	if m := mediaOf(req); m.Data != "" {
		t.Error("a body past the cap was inlined anyway")
	}
	var noted bool
	for _, n := range d.All() {
		if strings.Contains(n.Detail, "limit") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("the size refusal was not explained: %+v", d.All())
	}
}

// A failed download must cost one attachment, not the conversation.
func TestAFailedFetchLeavesTheRequestUsable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	req := imageRequest(srv.URL + "/missing.png")
	d := canonical.NewDiagnostics()
	localFetcher(t, 0).Inline(context.Background(), req, d)

	if len(req.Messages) != 1 || req.Messages[0].TextContent() != "look at this" {
		t.Errorf("the rest of the message was damaged: %+v", req.Messages)
	}
	if m := mediaOf(req); m.URL == "" {
		t.Error("a failed fetch should leave the url in place for the codec to report")
	}
	if !d.Lossy() {
		t.Error("a failed fetch must be recorded")
	}
}

// The gateway hands the same canonical request to a second provider when the
// first fails. Inlining must not rewrite what that attempt sees.
func TestInliningDoesNotMutateTheOriginalRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, "bytes")
	}))
	defer srv.Close()

	original := imageRequest(srv.URL + "/a.png")
	// The gateway's copy is shallow, exactly as in attempt.run.
	attempt := *original

	localFetcher(t, 0).Inline(context.Background(), &attempt, canonical.NewDiagnostics())

	if m := mediaOf(&attempt); m.Data == "" {
		t.Fatal("the attempt's copy was not inlined")
	}
	if m := mediaOf(original); m.Data != "" || m.URL == "" {
		t.Errorf("the original request was rewritten; a fallback attempt would see "+
			"the previous attempt's bytes: %+v", m)
	}
}

func TestBlockedCoversTheRangesThatMatter(t *testing.T) {
	for _, ip := range []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.0.1",
		"169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1", "fe80::1",
	} {
		if !blocked(net.ParseIP(ip)) {
			t.Errorf("%s must be refused", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700::1111"} {
		if blocked(net.ParseIP(ip)) {
			t.Errorf("%s is a normal public address and must be allowed", ip)
		}
	}
}
