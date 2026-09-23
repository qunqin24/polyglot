package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/qunqin24/polyglot/internal/store"
)

const liveEndpoint = "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

func TestLiveRoutesSetupAndAudioThroughCanonical(t *testing.T) {
	received := make(chan []byte, 2)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != liveEndpoint || r.Header.Get("x-goog-api-key") != "sk-upstream-secret-value" {
			t.Errorf("upstream path/auth = %q / %q", r.URL.Path, r.Header.Get("x-goog-api-key"))
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for i := 0; i < 2; i++ {
			_, b, err := conn.Read(r.Context())
			if err != nil {
				t.Error(err)
				return
			}
			received <- b
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"setupComplete":{}}`)); err != nil {
			t.Error(err)
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm","data":"AAE="}}]},"interrupted":true},"usageMetadata":{"promptTokenCount":8,"responseTokenCount":3}}`))
		<-r.Context().Done()
	}, "gemini")
	url := strings.Replace(h.server.URL, "http://", "ws://", 1) + liveEndpoint
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	header := http.Header{"X-Goog-Api-Key": []string{h.clientKey}}
	client, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	for _, frame := range []string{
		`{"setup":{"model":"models/my-model","generationConfig":{"responseModalities":["AUDIO"],"temperature":0.5}}}`,
		`{"realtimeInput":{"audio":{"mimeType":"audio/pcm;rate=16000","data":"AAEC"}}}`,
	} {
		if err := client.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		_, b, err := client.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && (!strings.Contains(string(b), `"audio/pcm"`) || !strings.Contains(string(b), `"interrupted":true`)) {
			t.Fatalf("output lost audio/interruption: %s", b)
		}
	}
	setup, audio := <-received, <-received
	var first struct {
		Setup struct {
			Model            string `json:"model"`
			GenerationConfig struct {
				Temperature float64 `json:"temperature"`
			} `json:"generationConfig"`
		} `json:"setup"`
	}
	if err := json.Unmarshal(setup, &first); err != nil {
		t.Fatal(err)
	}
	if first.Setup.Model != "models/upstream-model-x" || first.Setup.GenerationConfig.Temperature != 0.5 {
		t.Fatalf("setup not routed or preserved: %s", setup)
	}
	if !strings.Contains(string(audio), `"AAEC"`) {
		t.Fatalf("audio not forwarded: %s", audio)
	}
	if err := client.Close(websocket.StatusNormalClosure, "done"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); {
		logs, err := h.team().ListRequestLogs(context.Background(), store.LogFilter{Protocol: "gemini-live", Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(logs) == 1 {
			if logs[0].Status != "success" || logs[0].InputTokens != 8 || logs[0].OutputTokens != 3 ||
				logs[0].UpstreamModel != "upstream-model-x" || logs[0].APIKeyName != "test" {
				t.Fatalf("Live request log = %+v", logs[0])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Live session produced no request log")
}

func TestLiveRejectsUnauthenticatedAndUnknownModels(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {
		t.Error("unexpected upstream connection")
	}, "gemini")
	url := strings.Replace(h.server.URL, "http://", "ws://", 1) + liveEndpoint
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("unauthenticated dial: %v / %+v", err, resp)
	}
	client, _, err := websocket.Dial(ctx, url+"?key="+h.clientKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	if err := client.Write(ctx, websocket.MessageText, []byte(`{"setup":{"model":"models/not-registered"}}`)); err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("unknown model close = %v", err)
	}
}
