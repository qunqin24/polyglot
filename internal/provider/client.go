package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// Client is the shared outbound HTTP client. There is exactly one per
// process: creating a Transport per request would discard connection pooling
// and destroy latency under load.
type Client struct {
	http *http.Client
}

var blockPrivate = os.Getenv("POLYGLOT_BLOCK_PRIVATE_UPSTREAM") == "true" ||
	os.Getenv("BLOCK_PRIVATE_UPSTREAM") == "true"

// NewClient builds the process-wide client. Timeout is applied per request via
// context, not on the client, because streaming responses are long-lived.
func NewClient() *Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}

	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           guardedDial(dialer),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// No ResponseHeaderTimeout: some providers take minutes to emit the
		// first token on a large prompt. The request context bounds it.
		ReadBufferSize: 32 << 10,
	}

	return &Client{http: &http.Client{
		Transport: tr,
		// Never follow redirects to a different host: an upstream that
		// redirects could otherwise be used to leak the Authorization header.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// guardedDial optionally refuses connections to private ranges. It defaults to
// permissive because running Polyglot in front of a local Ollama or vLLM is a
// first-class use case; set BLOCK_PRIVATE_UPSTREAM=true when providers are
// operator-untrusted.
func guardedDial(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if blockPrivate {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if isPrivate(ip.IP) {
					return nil, fmt.Errorf("upstream %s resolves to blocked address %s", host, ip.IP)
				}
			}
		}
		return d.DialContext(ctx, network, addr)
	}
}

func isPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// Do sends the request. The caller owns closing resp.Body.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	// CheckRedirect stops at the first hop; surface it as an error rather
	// than handing the caller an HTML redirect page.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		return nil, fmt.Errorf("upstream returned redirect %d to %q; fix the provider base URL",
			resp.StatusCode, loc)
	}
	return resp, nil
}

// IsClientDisconnect reports whether an error is the client going away rather
// than a genuine upstream failure, so it is not logged as an error.
func IsClientDisconnect(err error) bool {
	return errors.Is(err, context.Canceled)
}
