package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
	"github.com/qunqin24/polyglot/internal/protocol"
)

func TestLiveDialsTheGeminiWebSocketRoot(t *testing.T) {
	for _, suffix := range []string{"", "/v1beta"} {
		t.Run(suffix, func(t *testing.T) {
			requested := make(chan string, 1)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested <- r.URL.Path + ":" + r.Header.Get("x-goog-api-key")
				conn, err := websocket.Accept(w, r, nil)
				if err == nil {
					conn.CloseNow()
				}
			}))
			defer up.Close()
			conn, err := NewClient().DialLive(context.Background(), &Target{
				Name: "gemini", Protocol: protocol.Gemini, BaseURL: up.URL + suffix, APIKey: "upstream-key",
			})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if got := <-requested; got != "/"+livePath+":upstream-key" {
				t.Fatalf("Live path/auth = %q", got)
			}
		})
	}
}
