package api

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/qunqin24/polyglot/internal/provider"
	"github.com/qunqin24/polyglot/internal/store"
)

// seenCall is what a fake multi-protocol upstream recorded for one request.
type seenCall struct {
	Path   string
	Header http.Header
	Body   string
}

// vendorUpstream imitates a vendor that serves one account over several
// protocols: a model list at /models naming each model's protocol, and a
// canned reply per endpoint.
func vendorUpstream(listing string, log *callLog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		log.mu.Lock()
		log.calls = append(log.calls, seenCall{Path: r.URL.Path, Header: r.Header.Clone(), Body: string(b)})
		log.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			io.WriteString(w, listing)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		case strings.HasSuffix(r.URL.Path, "/responses"):
			io.WriteString(w, `{"id":"r1","object":"response","status":"completed","model":"x","output":[{"type":"message","id":"o1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		default:
			io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}
	}
}

func (c *callLog) last(t *testing.T) seenCall {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) == 0 {
		t.Fatal("upstream saw no request")
	}
	return c.calls[len(c.calls)-1]
}

type callLog struct {
	mu    sync.Mutex
	calls []seenCall
}

const commandCodeListing = `{"object":"list","data":[
 {"id":"claude-sonnet-5","name":"Claude Sonnet 5","supported_endpoints":["/messages"]},
 {"id":"zai-org/GLM-5","name":"GLM 5","supported_endpoints":["/chat/completions","/responses"]},
 {"id":"gpt-5.5","name":"GPT 5.5","supported_endpoints":["/chat/completions","/responses"]}]}`

// One entry, one key, and every model on the protocol it is native to — with
// the client free to call any of them over any protocol it likes.
func TestVendorProviderRoutesEachModelOnItsOwnProtocol(t *testing.T) {
	log := &callLog{}
	h := newHarness(t, vendorUpstream(commandCodeListing, log), "openai")
	restore, err := provider.RebaseVendor(provider.VendorCommandCode, h.upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	admin := h.adminSession(t)

	// Discovery names the protocol of every model; the operator is never asked.
	var offer struct {
		OK     bool `json:"ok"`
		Models []struct {
			ID       string `json:"id"`
			Protocol string `json:"protocol"`
		} `json:"models"`
	}
	admin.send(t, http.MethodPost, "/api/providers/discover", map[string]any{
		"vendor": "commandcode", "protocol": "ignored", "base_url": "https://ignored.invalid",
		"api_key": "cc-key",
	}, &offer)
	if !offer.OK {
		t.Fatalf("discovery failed: %+v", offer)
	}
	got := map[string]string{}
	for _, m := range offer.Models {
		got[m.ID] = m.Protocol
	}
	want := map[string]string{"claude-sonnet-5": "anthropic", "zai-org/GLM-5": "openai", "gpt-5.5": "openai-responses"}
	for id, p := range want {
		if got[id] != p {
			t.Errorf("discovered protocol of %s = %q, want %q", id, got[id], p)
		}
	}
	// The discovery call authenticated with Bearer.
	if a := log.last(t).Header.Get("Authorization"); a != "Bearer cc-key" {
		t.Errorf("model listing Authorization = %q", a)
	}

	var created struct {
		Provider    store.Provider `json:"provider"`
		ModelsAdded int            `json:"models_added"`
	}
	admin.send(t, http.MethodPost, "/api/providers", map[string]any{
		"name": "Command Code", "vendor": "commandcode", "protocol": "", "base_url": "",
		"api_key": "cc-key", "headers": map[string]string{}, "enabled": true,
		"models": []map[string]string{{"id": "claude-sonnet-5"}, {"id": "zai-org/GLM-5"}, {"id": "gpt-5.5"}},
	}, &created)
	if created.ModelsAdded != 3 || created.Provider.Vendor != "commandcode" {
		t.Fatalf("created = %+v", created)
	}

	// The registry shows the protocol each model actually runs on.
	var listed struct {
		Models []store.Model `json:"models"`
	}
	admin.send(t, http.MethodGet, "/api/models?provider_id="+itoa(created.Provider.ID), nil, &listed)
	for _, m := range listed.Models {
		if m.Protocol != want[m.UpstreamModelID] {
			t.Errorf("registry protocol of %s = %q, want %q", m.UpstreamModelID, m.Protocol, want[m.UpstreamModelID])
		}
	}

	cases := []struct {
		model, path, ctype string
	}{
		{"claude-sonnet-5", "/provider/v1/messages", "anthropic"},
		{"zai-org/GLM-5", "/provider/v1/chat/completions", "openai"},
		{"gpt-5.5", "/provider/v1/responses", "responses"},
	}
	for _, tc := range cases {
		resp := h.post("/v1/chat/completions", chat(tc.model), nil)
		body := readAll(t, resp)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ok"`) {
			t.Fatalf("%s: status %d body %s", tc.model, resp.StatusCode, body)
		}
		call := log.last(t)
		if call.Path != tc.path {
			t.Errorf("%s went to %s, want %s", tc.model, call.Path, tc.path)
		}
		if call.Header.Get("Authorization") != "Bearer cc-key" {
			t.Errorf("%s: Authorization = %q, want Bearer", tc.model, call.Header.Get("Authorization"))
		}
		if tc.ctype == "anthropic" && call.Header.Get("anthropic-version") == "" {
			t.Errorf("%s: not an Anthropic request", tc.model)
		}
	}

	// The same Claude model, asked for through the Anthropic client protocol,
	// is a straight pass-through to /messages.
	resp := h.post("/v1/messages", `{"model":"claude-sonnet-5","max_tokens":8,"messages":[{"role":"user","content":"x"}]}`, nil)
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("anthropic client: %d %s", resp.StatusCode, body)
	}
	if p := log.last(t).Path; p != "/provider/v1/messages" {
		t.Errorf("anthropic client went to %s", p)
	}
}

func TestOpenCodeGoReadsProtocolFromListingAndSendsSession(t *testing.T) {
	listing := `{"object":"list","data":[
 {"id":"glm-5.3","name":"GLM-5.3","type":"chat","recommended_api":"chat-completions","api":{"chat-completions":{}}},
 {"id":"gpt-6-luna","name":"GPT 6 Luna","type":"chat","recommended_api":"responses","api":{"responses":{}}},
 {"id":"minimax-m3","name":"MiniMax M3","type":"chat","recommended_api":"messages","api":{"messages":{}}},
 {"id":"image-model","type":"image","recommended_api":"images","api":{}}]}`
	log := &callLog{}
	h := newHarness(t, vendorUpstream(listing, log), "openai")
	restore, err := provider.RebaseVendor(provider.VendorOpenCodeGo, h.upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	admin := h.adminSession(t)

	var created struct {
		Provider store.Provider `json:"provider"`
	}
	admin.send(t, http.MethodPost, "/api/providers", map[string]any{
		"name": "OpenCode Go", "vendor": "opencode-go", "api_key": "oc_sk_test",
		"headers": map[string]string{}, "enabled": true,
		"models": []map[string]string{{"id": "glm-5.3"}, {"id": "gpt-6-luna"}, {"id": "minimax-m3"}},
	}, &created)

	cases := map[string]string{
		"glm-5.3":    "/inference/go/openai/v1/chat/completions",
		"gpt-6-luna": "/inference/go/openai/v1/responses",
		"minimax-m3": "/inference/go/anthropic/v1/messages",
	}
	var session string
	for model, path := range cases {
		resp := h.post("/v1/chat/completions", chat(model), nil)
		if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", model, resp.StatusCode, body)
		}
		call := log.last(t)
		if call.Path != path {
			t.Errorf("%s went to %s, want %s", model, call.Path, path)
		}
		if call.Header.Get("Authorization") != "Bearer oc_sk_test" {
			t.Errorf("%s: Authorization = %q", model, call.Header.Get("Authorization"))
		}
		s := call.Header.Get("x-opencode-session")
		if !strings.HasPrefix(s, "ses_") {
			t.Errorf("%s: x-opencode-session = %q", model, s)
		}
		if session != "" && s != session {
			t.Errorf("session id changed between requests: %q then %q", session, s)
		}
		session = s
	}
}

// A provider without a vendor keeps every old behaviour: its own protocol, its
// own base URL, and no Bearer on Anthropic upstreams that never asked for it.
func TestOrdinaryProviderIsUnaffectedByVendors(t *testing.T) {
	log := &callLog{}
	h := newHarness(t, vendorUpstream(`{"data":[]}`, log), "anthropic")
	resp := h.post("/v1/chat/completions", chat("my-model"), nil)
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	call := log.last(t)
	if call.Path != "/v1/messages" {
		t.Errorf("path = %s, want /v1/messages", call.Path)
	}
	if call.Header.Get("x-api-key") == "" || call.Header.Get("Authorization") != "" {
		t.Errorf("auth headers = x-api-key %q, Authorization %q", call.Header.Get("x-api-key"), call.Header.Get("Authorization"))
	}
	if call.Header.Get("x-opencode-session") != "" {
		t.Error("an ordinary provider must not send a vendor header")
	}
}
