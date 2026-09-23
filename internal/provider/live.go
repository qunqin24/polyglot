package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/qunqin24/polyglot/internal/protocol"
)

const livePath = "ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

// DialLive uses the process-wide HTTP transport and its guarded dialer. The
// provider supplies a Gemini API base URL (not the WebSocket endpoint).
func (c *Client) DialLive(ctx context.Context, t *Target) (*websocket.Conn, error) {
	if t.Protocol != protocol.Gemini {
		return nil, fmt.Errorf("provider %q does not speak Gemini Live", t.Name)
	}
	if err := ValidateBaseURL(t.BaseURL); err != nil {
		return nil, fmt.Errorf("provider %q: %w", t.Name, err)
	}
	u, _ := url.Parse(t.BaseURL)
	// Gemini's HTTP generation base may include /v1beta; its WebSocket path
	// branches from the API root, not from the generation version prefix.
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1beta")
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + livePath
	u.RawPath = ""
	h := http.Header{}
	h.Set("x-goog-api-key", t.APIKey)
	h.Set("User-Agent", "Polyglot/0.1")
	// Custom headers may be required by operator-configured Gemini services;
	// they cannot override the gateway's upstream credential.
	req := &http.Request{Header: h}
	applyCustom(req, t)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient: c.http, HTTPHeader: req.Header,
	})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		// The dial error may include response headers, URL query parameters,
		// or an upstream body. None are safe for a client or request log.
		return nil, fmt.Errorf("Gemini Live connection failed (HTTP %d)", liveStatus(resp))
	}
	return conn, nil
}

func liveStatus(resp *http.Response) int {
	if resp != nil {
		return resp.StatusCode
	}
	return 0
}
