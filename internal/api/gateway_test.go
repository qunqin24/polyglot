package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/auth"
	"github.com/qunqin24/polyglot/internal/config"
	"github.com/qunqin24/polyglot/internal/pricing"
	"github.com/qunqin24/polyglot/internal/provider"
	"github.com/qunqin24/polyglot/internal/setup"
	"github.com/qunqin24/polyglot/internal/store"
	"github.com/qunqin24/polyglot/internal/telemetry"
	"github.com/qunqin24/polyglot/internal/usage"
)

// harness is a full Polyglot instance on a temp database, pointed at a fake
// upstream. It exercises the real pipeline, not a mock of it.
type harness struct {
	t         *testing.T
	health    *provider.Health
	server    *httptest.Server
	store     *store.Store
	clientKey string
	upstream  *httptest.Server
	telemetry *telemetry.Telemetry
	prices    *pricing.Resolver
}

const testSetupToken = "test-setup-token-value"

// harnessOpt tweaks the instance before it starts. Everything the default
// harness builds — one provider, one alias, one API key — stays as it is, so
// existing tests are unaffected by anything added here.
type harnessOpt func(*harnessConfig)

type harnessConfig struct {
	telemetry telemetry.Config
	// setup runs after the store is open and before the server starts, for
	// tests that need a second provider or a different routing shape.
	setup func(t *testing.T, st *store.Store, defaultProviderID int64)
	// tweak adjusts the process configuration before the server is built.
	tweak func(*config.Config)
}

// withTelemetry replaces the harness's telemetry configuration.
func withTelemetry(c telemetry.Config) harnessOpt {
	return func(h *harnessConfig) { h.telemetry = c }
}

// withConfig adjusts the process configuration the harness serves with.
func withConfig(fn func(*config.Config)) harnessOpt {
	return func(h *harnessConfig) { h.tweak = fn }
}

func withSetup(fn func(t *testing.T, st *store.Store, defaultProviderID int64)) harnessOpt {
	return func(h *harnessConfig) { h.setup = fn }
}

func newHarness(t *testing.T, upstreamHandler http.HandlerFunc, protocolName string, opts ...harnessOpt) *harness {
	t.Helper()

	hc := &harnessConfig{
		// Telemetry is off unless a test asks for it, so the tests that are
		// about protocol conversion stay about protocol conversion.
		telemetry: telemetry.Config{},
	}
	for _, o := range opts {
		o(hc)
	}

	upstream := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.Config{
		DataDir:          dir,
		MaxRequestBytes:  1 << 20,
		MaxUpstreamBytes: 1 << 20,
		UpstreamTimeout:  10 * time.Second,
		Telemetry:        hc.telemetry,
	}
	if hc.tweak != nil {
		hc.tweak(cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The real price path, including the catalog the binary ships with, so a
	// test sees the costs an operator would.
	prices := pricing.NewResolver(st)
	if snap, err := pricing.Embedded(); err != nil {
		t.Fatalf("embedded price catalog: %v", err)
	} else if err := st.LoadEmbeddedCatalog(context.Background(), snap); err != nil {
		t.Fatalf("load price catalog: %v", err)
	}
	if err := prices.Reload(context.Background()); err != nil {
		t.Fatalf("load prices: %v", err)
	}

	ul := usage.New(st, log, 0, prices)
	ctx, cancel := context.WithCancel(context.Background())
	go ul.Run(ctx)
	t.Cleanup(func() { cancel(); ul.Wait(time.Second) })

	ctxBg := context.Background()
	p, err := st.CreateProvider(ctxBg, &store.Provider{
		Name:     "fake",
		Protocol: protocolName,
		BaseURL:  upstream.URL,
		APIKey:   "sk-upstream-secret-value",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	// "my-model" is an alias, which exercises the optional naming layer.
	if _, err := st.CreateAlias(ctxBg, &store.ModelAlias{
		Alias:         "my-model",
		ProviderID:    p.ID,
		UpstreamModel: "upstream-model-x",
		Enabled:       true,
	}); err != nil {
		t.Fatalf("create alias: %v", err)
	}

	plaintext, prefix, hash := auth.NewAPIKey()
	if _, err := st.CreateAPIKey(ctxBg, "test", prefix, hash); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	if hc.setup != nil {
		hc.setup(t, st, p.ID)
	}

	tel := telemetry.New(hc.telemetry, log)
	t.Cleanup(func() { tel.Shutdown(time.Second) })

	setupGuard, err := setup.New(testSetupToken)
	if err != nil {
		t.Fatalf("setup guard: %v", err)
	}
	apiServer := NewServer(st, cfg, log, ul, tel, prices, setupGuard)
	srv := httptest.NewServer(apiServer.Handler())
	t.Cleanup(srv.Close)

	return &harness{
		t: t, server: srv, store: st, clientKey: plaintext, health: apiServer.health,
		upstream: upstream, telemetry: tel, prices: prices,
	}
}

// serverHealth reaches the provider health tracker the server built, so a test
// can put a provider into cooldown without making it fail for real.
func (h *harness) serverHealth() *provider.Health { return h.health }

func (h *harness) post(path, body string, header map[string]string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.server.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.clientKey)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("do request: %v", err)
	}
	return resp
}

// adminSession signs in as the administrator and returns a client that carries
// the session cookie and the matching CSRF token, exactly as the WebUI does.
type adminClient struct {
	base    string
	cookies []*http.Cookie
	csrf    string
}

func (h *harness) adminSession(t *testing.T) *adminClient {
	t.Helper()

	hash, err := auth.HashPassword("test-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if _, err := h.store.CreateAdmin(context.Background(), "admin", hash); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	resp, err := http.Post(h.server.URL+"/api/auth/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"test-password"}`))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", resp.StatusCode)
	}

	c := &adminClient{base: h.server.URL, cookies: resp.Cookies()}
	for _, ck := range c.cookies {
		if ck.Name == auth.CSRFCookie {
			c.csrf = ck.Value
		}
	}
	if c.csrf == "" {
		t.Fatal("login did not set a CSRF cookie")
	}
	return c
}

func (c *adminClient) do(t *testing.T, method, path string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeader, c.csrf)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func (c *adminClient) get(t *testing.T, path string, out any) {
	t.Helper()
	resp := c.do(t, http.MethodGet, path, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s = %d: %s", path, resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func (c *adminClient) send(t *testing.T, method, path string, payload any, out any) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	resp := c.do(t, method, path, bytes.NewReader(b))
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s = %d: %s", method, path, resp.StatusCode, body)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
}

// httptestServer starts an extra upstream and returns its URL. Used by tests
// that need more than the one the harness creates.
func httptestServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestOpenAIToOpenAINonStreaming(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"id":"chatcmpl-up","object":"chat.completion","created":1700000000,"model":"upstream-model-x",
			"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}
		}`)
	}, "openai")

	resp := h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"ping"}]}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("upstream path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-upstream-secret-value" {
		t.Errorf("upstream auth header = %q", gotAuth)
	}
	// The alias must be replaced by the upstream model name on the way out.
	if !strings.Contains(gotBody, `"model":"upstream-model-x"`) {
		t.Errorf("upstream body did not carry the mapped model: %s", gotBody)
	}

	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("client response is not JSON: %v (%s)", err, body)
	}
	// The client asked for the alias, so the alias is what it gets back.
	if out.Model != "my-model" {
		t.Errorf("model = %q, want the requested alias", out.Model)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "pong" {
		t.Errorf("choices = %+v", out.Choices)
	}
	if out.Usage.PromptTokens != 7 {
		t.Errorf("usage not propagated: %+v", out.Usage)
	}
}

func TestOpenAIToOpenAIStreaming(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["stream"] != true {
			t.Errorf("upstream did not receive stream=true: %+v", req)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		for _, chunk := range []string{
			`{"id":"c1","object":"chat.completion.chunk","model":"upstream-model-x","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		} {
			io.WriteString(w, "data: "+chunk+"\n\n")
			f.Flush()
		}
		io.WriteString(w, "data: [DONE]\n\n")
		f.Flush()
	}, "openai")

	resp := h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Errorf("stream must end with [DONE]:\n%s", body)
	}

	var text strings.Builder
	var model string
	sawUsage := false
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("chunk is not valid JSON: %v (%s)", err, payload)
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		for _, c := range chunk.Choices {
			text.WriteString(c.Delta.Content)
		}
		if chunk.Usage != nil && chunk.Usage.TotalTokens == 5 {
			sawUsage = true
		}
	}
	if text.String() != "Hello" {
		t.Errorf("assembled text = %q", text.String())
	}
	// A streamed reply must name the alias, exactly as a buffered one does.
	if model != "my-model" {
		t.Errorf("streamed chunks report model %q, want the requested alias", model)
	}
	if !sawUsage {
		t.Errorf("include_usage was requested but no usage chunk arrived:\n%s", body)
	}
}

func TestRequestIsLogged(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1}}`)
	}, "openai")

	readAll(t, h.post("/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"x"}]}`, nil))

	// The logger flushes on a timer; give it a moment.
	var logs []*store.RequestLog
	for range 40 {
		time.Sleep(100 * time.Millisecond)
		var err error
		logs, err = h.store.ListRequestLogs(context.Background(), store.LogFilter{Limit: 10})
		if err != nil {
			t.Fatalf("list logs: %v", err)
		}
		if len(logs) > 0 {
			break
		}
	}
	if len(logs) != 1 {
		t.Fatalf("want exactly one log row per request, got %d", len(logs))
	}
	l := logs[0]
	if l.Status != "success" || l.ProviderName != "fake" {
		t.Errorf("log = %+v", l)
	}
	if l.ModelAlias != "my-model" || l.UpstreamModel != "upstream-model-x" {
		t.Errorf("log models = %q -> %q", l.ModelAlias, l.UpstreamModel)
	}
	if l.InputTokens != 4 || l.OutputTokens != 1 {
		t.Errorf("log usage = %d/%d", l.InputTokens, l.OutputTokens)
	}
	if l.ClientProtocol != "openai" || l.UpstreamProtocol != "openai" {
		t.Errorf("log protocols = %q -> %q", l.ClientProtocol, l.UpstreamProtocol)
	}
}

func TestUpstreamErrorDoesNotLeakCredentials(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		// A hostile or careless upstream echoing our key back.
		io.WriteString(w, `{"error":{"message":"bad key sk-upstream-secret-value","type":"invalid_request_error"}}`)
	}, "openai")

	resp := h.post("/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"x"}]}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if strings.Contains(body, "sk-upstream-secret-value") {
		t.Fatalf("upstream credential leaked to the client: %s", body)
	}
	if !strings.Contains(body, `"error"`) {
		t.Errorf("error not in OpenAI shape: %s", body)
	}
}

func TestMissingAndBadAPIKey(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called for an unauthenticated request")
	}, "openai")

	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"my-model","messages":[]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body := readAll(t, resp); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no key: status = %d, body = %s", resp.StatusCode, body)
	}

	resp = h.post("/v1/chat/completions", `{"model":"my-model","messages":[]}`,
		map[string]string{"Authorization": "Bearer pg_wrong"})
	if body := readAll(t, resp); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad key: status = %d, body = %s", resp.StatusCode, body)
	}
}

func TestUnknownModelIsRejected(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called for an unmapped model")
	}, "openai")

	resp := h.post("/v1/chat/completions", `{"model":"nope","messages":[{"role":"user","content":"x"}]}`, nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, body = %s", resp.StatusCode, body)
	}
	// The message must tell the operator what to actually do about it.
	for _, want := range []string{"not found", "Add the model on its provider", "alias"} {
		if !strings.Contains(body, want) {
			t.Errorf("error message is not actionable, missing %q: %s", want, body)
		}
	}
}

func TestListModels(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")

	req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+h.clientKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"my-model"`) {
		t.Errorf("model list does not contain the alias: %s", body)
	}
}
